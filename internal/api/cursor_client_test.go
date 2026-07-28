package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewCursorClient(t *testing.T) {
	logger := slog.Default()
	client := NewCursorClient("test_token", logger)
	if client == nil {
		t.Fatal("NewCursorClient returned nil")
	}
	if client.baseURL != CursorBaseURL {
		t.Errorf("baseURL = %q, want %q", client.baseURL, CursorBaseURL)
	}
}

func TestNewCursorClient_WithOptions(t *testing.T) {
	logger := slog.Default()
	client := NewCursorClient("test_token", logger,
		WithCursorBaseURL("http://localhost:1234"),
		WithCursorTimeout(5*time.Second),
	)
	if client.baseURL != "http://localhost:1234" {
		t.Errorf("baseURL = %q, want custom", client.baseURL)
	}
}

func TestCursorClient_SetToken(t *testing.T) {
	logger := slog.Default()
	client := NewCursorClient("initial_token", logger)

	client.SetToken("new_token")
	if client.getToken() != "new_token" {
		t.Errorf("getToken() = %q, want %q", client.getToken(), "new_token")
	}
}

func TestCursorClient_FetchQuotas_IndividualSuccess(t *testing.T) {
	usageHandled := false
	planInfoHandled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/aiserver.v1.DashboardService/GetCurrentPeriodUsage" {
			usageHandled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"billingCycleStart": "1768399334000",
				"billingCycleEnd": "1771077734000",
				"planUsage": {
					"totalSpend": 5000,
					"remaining": 35000,
					"limit": 40000,
					"totalPercentUsed": 12.5,
					"autoPercentUsed": 3.0,
					"apiPercentUsed": 9.5
				},
				"enabled": true
			}`))
			return
		}
		if r.URL.Path == "/aiserver.v1.DashboardService/GetPlanInfo" {
			planInfoHandled = true
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"planInfo": {
					"planName": "Pro",
					"includedAmountCents": 2000,
					"price": "$20/mo"
				}
			}`))
			return
		}
		if r.URL.Path == "/aiserver.v1.DashboardService/GetCreditGrantsBalance" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"hasCreditGrants": true,
				"totalCents": "5000",
				"usedCents": "2000"
			}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	logger := slog.Default()
	client := NewCursorClient("test_token", logger, WithCursorBaseURL(server.URL))
	snapshot, err := client.FetchQuotas(context.Background())
	if err != nil {
		t.Fatalf("FetchQuotas: %v", err)
	}

	if !usageHandled {
		t.Error("usage endpoint not called")
	}
	if !planInfoHandled {
		t.Error("plan info endpoint not called")
	}
	if snapshot.AccountType != CursorAccountIndividual {
		t.Errorf("AccountType = %q, want %q", snapshot.AccountType, CursorAccountIndividual)
	}
	if snapshot.PlanName != "Pro" {
		t.Errorf("PlanName = %q, want %q", snapshot.PlanName, "Pro")
	}
	if len(snapshot.Quotas) == 0 {
		t.Error("Expected at least one quota")
	}
}

func TestCursorClient_FetchQuotas_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	logger := slog.Default()
	client := NewCursorClient("bad_token", logger, WithCursorBaseURL(server.URL))
	_, err := client.FetchQuotas(context.Background())
	if err == nil {
		t.Error("Expected error for 401")
	}
	if !IsCursorAuthError(err) {
		t.Errorf("Expected auth error, got %v", err)
	}
}

func TestCursorClient_FetchQuotas_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	logger := slog.Default()
	client := NewCursorClient("test_token", logger, WithCursorBaseURL(server.URL))
	_, err := client.FetchQuotas(context.Background())
	if err == nil {
		t.Error("Expected error for 500")
	}
}

func TestCursorClient_ConnectRPCHeaders(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"billingCycleStart": "1768399334000",
			"billingCycleEnd": "1771077734000",
			"planUsage": {
				"totalSpend": 5000,
				"remaining": 35000,
				"limit": 40000,
				"totalPercentUsed": 12.5
			},
			"enabled": true
		}`))
	}))
	defer server.Close()

	logger := slog.Default()
	client := NewCursorClient("test_bearer_token", logger, WithCursorBaseURL(server.URL))
	_, _ = client.FetchQuotas(context.Background())

	if receivedHeaders.Get("Authorization") != "Bearer test_bearer_token" {
		t.Errorf("Authorization = %q, want %q", receivedHeaders.Get("Authorization"), "Bearer test_bearer_token")
	}
	if receivedHeaders.Get("Connect-Protocol-Version") != "1" {
		t.Errorf("Connect-Protocol-Version = %q, want %q", receivedHeaders.Get("Connect-Protocol-Version"), "1")
	}
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", receivedHeaders.Get("Content-Type"))
	}
}

func TestCursorClient_RedirectToken(t *testing.T) {
	logger := slog.Default()
	client := NewCursorClient("initial", logger)

	if client.getToken() != "initial" {
		t.Errorf("Initial token = %q, want %q", client.getToken(), "initial")
	}

	client.SetToken("updated")
	if client.getToken() != "updated" {
		t.Errorf("Updated token = %q, want %q", client.getToken(), "updated")
	}
}

func TestRedactCursorToken(t *testing.T) {
	tests := []struct {
		token    string
		expected string
	}{
		{"", "(empty)"},
		{"abc", "***...***"},
		{"eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9", "eyJh***...***CJ9"},
	}

	for _, tt := range tests {
		got := redactCursorToken(tt.token)
		if got != tt.expected {
			t.Errorf("redactCursorToken(%q) = %q, want %q", tt.token, got, tt.expected)
		}
	}
}

func TestRefreshCursorToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"access_token": "new_access_token",
			"id_token": "new_id_token",
			"refresh_token": "new_refresh_token",
			"shouldLogout": false
		}`))
	}))
	defer server.Close()

	cursorOAuthURL = server.URL
	defer func() { cursorOAuthURL = "https://api2.cursor.sh/oauth/token" }()

	resp, err := RefreshCursorToken(context.Background(), "old_refresh_token")
	if err != nil {
		t.Fatalf("RefreshCursorToken: %v", err)
	}
	if resp.AccessToken != "new_access_token" {
		t.Errorf("AccessToken = %q, want %q", resp.AccessToken, "new_access_token")
	}
	if resp.RefreshToken != "new_refresh_token" {
		t.Errorf("RefreshToken = %q, want %q", resp.RefreshToken, "new_refresh_token")
	}
	if resp.ShouldLogout {
		t.Error("ShouldLogout should be false")
	}
}

func TestRefreshCursorToken_SessionExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"access_token": "",
			"id_token": "",
			"shouldLogout": true
		}`))
	}))
	defer server.Close()

	cursorOAuthURL = server.URL
	defer func() { cursorOAuthURL = "https://api2.cursor.sh/oauth/token" }()

	_, err := RefreshCursorToken(context.Background(), "expired_refresh_token")
	if err == nil {
		t.Error("Expected error for shouldLogout=true")
	}
	if !IsCursorSessionExpired(err) {
		t.Errorf("Expected ErrCursorSessionExpired, got %v", err)
	}
}

func TestRefreshCursorToken_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_request"}`))
	}))
	defer server.Close()

	cursorOAuthURL = server.URL
	defer func() { cursorOAuthURL = "https://api2.cursor.sh/oauth/token" }()

	_, err := RefreshCursorToken(context.Background(), "bad_token")
	if err == nil {
		t.Error("Expected error for HTTP 400")
	}
}

func TestShouldFetchCursorRequestBasedUsage(t *testing.T) {
	tests := []struct {
		name           string
		usage          *CursorUsageResponse
		normalizedPlan string
		want           bool
	}{
		{
			name: "missing plan info still attempts request-based usage",
			usage: &CursorUsageResponse{
				Enabled:   true,
				PlanUsage: &CursorPlanUsage{Limit: 0},
			},
			normalizedPlan: "",
			want:           true,
		},
		{
			name: "team account with zero plan limit attempts request-based usage",
			usage: &CursorUsageResponse{
				Enabled:   true,
				PlanUsage: &CursorPlanUsage{Limit: 0},
			},
			normalizedPlan: "team",
			want:           true,
		},
		{
			name: "disabled enterprise account attempts request-based usage",
			usage: &CursorUsageResponse{
				Enabled:   false,
				PlanUsage: nil,
			},
			normalizedPlan: "enterprise",
			want:           true,
		},
		{
			name: "standard usage skips request-based endpoint",
			usage: &CursorUsageResponse{
				Enabled:   true,
				PlanUsage: &CursorPlanUsage{Limit: 100},
			},
			normalizedPlan: "",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFetchCursorRequestBasedUsage(tt.usage, tt.normalizedPlan); got != tt.want {
				t.Fatalf("shouldFetchCursorRequestBasedUsage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldUseCursorRequestBasedUsage(t *testing.T) {
	usage := &CursorUsageResponse{
		Enabled:   true,
		PlanUsage: &CursorPlanUsage{Limit: 0},
	}
	requestUsage := &CursorRequestUsageResponse{
		Models: map[string]CursorModelUsage{
			"gpt-4.1": {NumRequests: 10, MaxRequestUsage: 100},
		},
	}

	if !shouldUseCursorRequestBasedUsage(usage, requestUsage) {
		t.Fatal("expected request-based usage to be used when the plan limit is unavailable")
	}
	if shouldUseCursorRequestBasedUsage(&CursorUsageResponse{Enabled: true, PlanUsage: &CursorPlanUsage{Limit: 100}}, requestUsage) {
		t.Fatal("did not expect request-based usage when a standard plan limit is available")
	}
	if !shouldUseCursorRequestBasedUsage(&CursorUsageResponse{Enabled: false, PlanUsage: nil}, requestUsage) {
		t.Fatal("expected request-based usage for a disabled enterprise usage response")
	}
}

// cursorTestToken builds an unsigned JWT carrying sub, which the cookie-authenticated
// cursor.com surfaces are keyed on.
func cursorTestToken(subject string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + subject + `"}`))
	return "eyJhbGciOiJub25lIn0." + payload + "."
}

// cursorTeamServer answers the surfaces a team seat needs. The usage and legacy request
// payloads are the real structural zeros an ORG_TOKEN_BASED_CONTRACT seat receives.
func cursorTeamServer(t *testing.T, hardLimitBody string, hardLimitStatus int, calls map[string]int) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/aiserver.v1.DashboardService/GetCurrentPeriodUsage":
			w.Write([]byte(`{"billingCycleStart":"1785264303937","billingCycleEnd":"1785264303937","displayThreshold":100}`))
		case "/aiserver.v1.DashboardService/GetPlanInfo":
			w.Write([]byte(`{"planInfo":{"planName":"Enterprise","price":"Custom","billingCycleEnd":"1785542400000"}}`))
		case "/aiserver.v1.DashboardService/GetAggregatedUsageEvents":
			w.Write([]byte(`{"totalCostCents":1234.567891,"aggregations":[{"modelIntent":"default","totalCents":1234.567891}]}`))
		case "/aiserver.v1.DashboardService/GetHardLimit":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read GetHardLimit body: %v", err)
			}
			var req struct {
				TeamID int64 `json:"teamId"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("unmarshal GetHardLimit body %q: %v", body, err)
			}
			if req.TeamID != 987654 {
				t.Errorf("GetHardLimit teamId = %d, want 987654 (without it the per-user cap is omitted)", req.TeamID)
			}
			if hardLimitStatus != http.StatusOK {
				w.WriteHeader(hardLimitStatus)
				return
			}
			w.Write([]byte(hardLimitBody))
		case "/api/auth/stripe":
			w.Write([]byte(`{"membershipType":"enterprise","isTeamMember":true,"teamId":987654,"teamMembershipType":"ORG_TOKEN_BASED_CONTRACT","customerBalance":0}`))
		case "/api/usage":
			w.Write([]byte(`{"gpt-4":{"numRequests":0,"numRequestsTotal":0,"numTokens":0,"maxTokenUsage":null,"maxRequestUsage":null},"startOfMonth":"2026-07-01T00:00:00.000Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestCursorClient_FetchQuotas_TeamContract(t *testing.T) {
	calls := map[string]int{}
	server := cursorTeamServer(t, `{"hardLimit":25000,"hardLimitPerUser":150,"perUserMonthlyLimitDollars":200}`, http.StatusOK, calls)
	defer server.Close()

	originalWebBase := cursorWebBaseURL
	cursorWebBaseURL = server.URL
	defer func() { cursorWebBaseURL = originalWebBase }()

	client := NewCursorClient(cursorTestToken("user_01TEAMSEAT"), slog.Default(), WithCursorBaseURL(server.URL))
	snapshot, err := client.FetchQuotas(context.Background())
	if err != nil {
		t.Fatalf("FetchQuotas: %v", err)
	}

	if len(snapshot.Quotas) != 1 {
		t.Fatalf("len(Quotas) = %d, want 1: %+v", len(snapshot.Quotas), snapshot.Quotas)
	}
	quota := snapshot.Quotas[0]
	if quota.Name != "total_usage" {
		t.Errorf("Name = %q, want total_usage", quota.Name)
	}
	if quota.Format != CursorFormatDollars {
		t.Errorf("Format = %q, want %q", quota.Format, CursorFormatDollars)
	}
	if math.Abs(quota.Used-12.34567891) > 1e-9 {
		t.Errorf("Used = %v, want 12.34567891 (totalCostCents/100)", quota.Used)
	}
	if quota.Limit != 200 {
		t.Errorf("Limit = %v, want 200 (perUserMonthlyLimitDollars)", quota.Limit)
	}
	if math.Abs(quota.Utilization-6.172839455) > 1e-6 {
		t.Errorf("Utilization = %v, want ~6.1728", quota.Utilization)
	}
	if quota.ResetsAt == nil {
		t.Fatal("ResetsAt = nil, want planInfo.billingCycleEnd")
	}
	if got := quota.ResetsAt.UTC().Format(time.RFC3339); got != "2026-08-01T00:00:00Z" {
		t.Errorf("ResetsAt = %s, want 2026-08-01T00:00:00Z", got)
	}

	if calls["/api/usage"] != 0 {
		t.Errorf("legacy per-user usage endpoint called %d times; it reads zero on a team seat", calls["/api/usage"])
	}
}

func TestCursorClient_FetchQuotas_TeamContractHardLimitFailure(t *testing.T) {
	calls := map[string]int{}
	server := cursorTeamServer(t, "", http.StatusInternalServerError, calls)
	defer server.Close()

	originalWebBase := cursorWebBaseURL
	cursorWebBaseURL = server.URL
	defer func() { cursorWebBaseURL = originalWebBase }()

	client := NewCursorClient(cursorTestToken("user_01TEAMSEAT"), slog.Default(), WithCursorBaseURL(server.URL))
	snapshot, err := client.FetchQuotas(context.Background())
	if err == nil {
		t.Fatalf("FetchQuotas succeeded with quotas %+v, want error so the last good snapshot is kept", snapshot.Quotas)
	}
	if snapshot != nil {
		t.Errorf("snapshot = %+v, want nil", snapshot)
	}
}

func TestCursorClient_FetchQuotas_TeamContractWithoutPerUserLimit(t *testing.T) {
	calls := map[string]int{}
	server := cursorTeamServer(t, `{"hardLimit":30000}`, http.StatusOK, calls)
	defer server.Close()

	originalWebBase := cursorWebBaseURL
	cursorWebBaseURL = server.URL
	defer func() { cursorWebBaseURL = originalWebBase }()

	client := NewCursorClient(cursorTestToken("user_01TEAMSEAT"), slog.Default(), WithCursorBaseURL(server.URL))
	if _, err := client.FetchQuotas(context.Background()); err == nil {
		t.Fatal("FetchQuotas succeeded without a per-user cap, want error rather than a fabricated quota")
	}
}

func TestCursorUsesTeamContract(t *testing.T) {
	orgContractStripe := &CursorStripeResponse{IsTeamMember: true, TeamID: 987654, TeamMembershipType: CursorOrgTokenContract}
	emptyUsage := &CursorUsageResponse{}

	tests := []struct {
		name   string
		usage  *CursorUsageResponse
		stripe *CursorStripeResponse
		want   bool
	}{
		{"org contract seat with empty cycle", emptyUsage, orgContractStripe, true},
		{"no stripe response", emptyUsage, nil, false},
		{"individual account", emptyUsage, &CursorStripeResponse{IsTeamMember: false}, false},
		{"team member without team id", emptyUsage, &CursorStripeResponse{IsTeamMember: true, TeamMembershipType: CursorOrgTokenContract}, false},
		{
			"team type we have not observed reading zero",
			emptyUsage,
			&CursorStripeResponse{IsTeamMember: true, TeamID: 987654, TeamMembershipType: "SEAT_BASED"},
			false,
		},
		{
			"seat billed against its own plan",
			&CursorUsageResponse{PlanUsage: &CursorPlanUsage{Limit: 40000}},
			orgContractStripe,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cursorUsesTeamContract(tt.usage, tt.stripe); got != tt.want {
				t.Errorf("cursorUsesTeamContract() = %v, want %v", got, tt.want)
			}
		})
	}
}
