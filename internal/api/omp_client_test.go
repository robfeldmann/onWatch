package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestOMPClient_FetchUsageSendsBearer(t *testing.T) {
	body := loadOMPFixture(t)
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Write(body)
	}))
	defer srv.Close()

	c := NewOMPClient(srv.URL+"/", "static-token", nil)
	usage, err := c.FetchUsage(context.Background())
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if len(usage.Reports) != 4 {
		t.Errorf("reports = %d, want 4", len(usage.Reports))
	}
	if gotPath != "/v1/usage" {
		t.Errorf("path = %q, want /v1/usage", gotPath)
	}
	if gotAuth != "Bearer static-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
}

func TestOMPClient_401RefreshesAndRetriesOnce(t *testing.T) {
	body := loadOMPFixture(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	var refreshes atomic.Int32
	c := NewOMPClient(srv.URL, "stale", nil)
	c.SetTokenRefresh(func() string {
		refreshes.Add(1)
		return "fresh"
	})
	if _, err := c.FetchUsage(context.Background()); err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if calls.Load() != 2 || refreshes.Load() != 1 {
		t.Errorf("calls=%d refreshes=%d, want 2 and 1", calls.Load(), refreshes.Load())
	}
}

func TestOMPClient_401PersistsAfterRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewOMPClient(srv.URL, "stale", nil)
	c.SetTokenRefresh(func() string { return "still-bad" })
	_, err := c.FetchUsage(context.Background())
	if !errors.Is(err, ErrOMPUnauthorized) {
		t.Fatalf("err = %v, want ErrOMPUnauthorized", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want exactly one retry", calls.Load())
	}
}

func TestOMPClient_401WithoutTokenFromHookDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewOMPClient(srv.URL, "stale", nil)
	c.SetTokenRefresh(func() string { return "" }) // command failed
	if _, err := c.FetchUsage(context.Background()); !errors.Is(err, ErrOMPUnauthorized) {
		t.Fatalf("err = %v, want ErrOMPUnauthorized", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestOMPClient_StatusErrors(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusTooManyRequests, "", ErrOMPRateLimited},
		{http.StatusBadGateway, "", ErrOMPServerError},
		{http.StatusNotFound, "", ErrOMPInvalidResponse},
		{http.StatusOK, `{"generatedAt":1}`, ErrOMPInvalidResponse},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		_, err := NewOMPClient(srv.URL, "", nil).FetchUsage(context.Background())
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}
}

func TestOMPClient_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := NewOMPClient(url, "", nil).FetchUsage(context.Background()); !errors.Is(err, ErrOMPNetworkError) {
		t.Fatalf("err = %v, want ErrOMPNetworkError", err)
	}
}
