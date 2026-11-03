package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	ompUsagePath       = "/v1/usage"
	ompRequestTimeout  = 30 * time.Second
	ompMaxResponseSize = 4 << 20
)

var (
	ErrOMPUnauthorized    = errors.New("omp: unauthorized")
	ErrOMPRateLimited     = errors.New("omp: rate limited")
	ErrOMPServerError     = errors.New("omp: server error")
	ErrOMPNetworkError    = errors.New("omp: network error")
	ErrOMPInvalidResponse = errors.New("omp: invalid response")
)

// OMPClient reads the OMP auth broker's /v1/usage endpoint. The bearer token
// can come from a refresh hook (OMP_BROKER_TOKEN_COMMAND); a 401 runs the hook
// and retries once.
type OMPClient struct {
	httpClient *http.Client
	logger     *slog.Logger
	usageURL   string

	mu      sync.Mutex
	token   string
	refresh func() string
}

// OMPClientOption configures an OMPClient.
type OMPClientOption func(*OMPClient)

// WithOMPHTTPTransport replaces the HTTP transport (tests).
func WithOMPHTTPTransport(rt http.RoundTripper) OMPClientOption {
	return func(c *OMPClient) {
		c.httpClient.Transport = rt
	}
}

// WithOMPTimeout overrides the per-request timeout.
func WithOMPTimeout(timeout time.Duration) OMPClientOption {
	return func(c *OMPClient) {
		c.httpClient.Timeout = timeout
	}
}

// NewOMPClient builds a client for the broker at baseURL (e.g.
// http://127.0.0.1:8765). token may be empty when a refresh hook supplies it.
func NewOMPClient(baseURL, token string, logger *slog.Logger, opts ...OMPClientOption) *OMPClient {
	if logger == nil {
		logger = slog.Default()
	}
	c := &OMPClient{
		httpClient: &http.Client{
			Timeout: ompRequestTimeout,
			// Never replay the bearer token to a redirect target.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				MaxIdleConns:          1,
				MaxIdleConnsPerHost:   1,
				ResponseHeaderTimeout: ompRequestTimeout,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
			},
		},
		logger:   logger,
		usageURL: strings.TrimRight(strings.TrimSpace(baseURL), "/") + ompUsagePath,
		token:    strings.TrimSpace(token),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// SetTokenRefresh installs the hook that yields a fresh bearer token. An empty
// result keeps the current token.
func (c *OMPClient) SetTokenRefresh(fn func() string) {
	c.mu.Lock()
	c.refresh = fn
	c.mu.Unlock()
}

// RefreshToken runs the refresh hook, if any, and reports whether it produced
// a token.
func (c *OMPClient) RefreshToken() bool {
	c.mu.Lock()
	fn := c.refresh
	c.mu.Unlock()
	if fn == nil {
		return false
	}
	token := strings.TrimSpace(fn())
	if token == "" {
		return false
	}
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return true
}

func (c *OMPClient) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// FetchUsage returns the parsed /v1/usage response. On 401 it refreshes the
// token through the hook and retries once.
func (c *OMPClient) FetchUsage(ctx context.Context) (*OMPUsage, error) {
	body, err := c.get(ctx)
	if errors.Is(err, ErrOMPUnauthorized) && c.RefreshToken() {
		c.logger.Info("OMP broker returned 401; retrying with refreshed token")
		body, err = c.get(ctx)
	}
	if err != nil {
		return nil, err
	}
	return ParseOMPUsage(body)
}

func (c *OMPClient) get(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.usageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("omp: creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "onwatch/1.0")
	if token := c.currentToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrOMPNetworkError, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		io.Copy(io.Discard, io.LimitReader(resp.Body, ompMaxResponseSize))
		return nil, fmt.Errorf("%w: HTTP %d", ErrOMPUnauthorized, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		io.Copy(io.Discard, io.LimitReader(resp.Body, ompMaxResponseSize))
		return nil, ErrOMPRateLimited
	case resp.StatusCode >= 500:
		io.Copy(io.Discard, io.LimitReader(resp.Body, ompMaxResponseSize))
		return nil, fmt.Errorf("%w: HTTP %d", ErrOMPServerError, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		io.Copy(io.Discard, io.LimitReader(resp.Body, ompMaxResponseSize))
		return nil, fmt.Errorf("%w: HTTP %d", ErrOMPInvalidResponse, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, ompMaxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading body: %v", ErrOMPNetworkError, err)
	}
	if len(body) > ompMaxResponseSize {
		return nil, fmt.Errorf("%w: body exceeds %d bytes", ErrOMPInvalidResponse, ompMaxResponseSize)
	}
	return body, nil
}
