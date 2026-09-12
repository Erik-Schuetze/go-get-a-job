package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpbody"
)

// Every connector reads an untrusted response body, so every connector needs
// the same guarantee: a body larger than the configured cap fails with a
// distinguishable "too large" error rather than being buffered in full.
// Timeouts bound how long a request may take; they say nothing about size.
func TestConnectors_RejectOversizedResponses(t *testing.T) {
	const cap = 256
	hugeBody := `{"padding":"` + strings.Repeat("a", cap*4) + `"}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(hugeBody))
	}))
	defer server.Close()

	ctx := context.Background()

	cases := []struct {
		name  string
		fetch func() (any, error)
	}{
		{
			name: "greenhouse",
			fetch: func() (any, error) {
				s := NewGreenhouse("acme", "Acme")
				s.BaseURL, s.HTTPClient, s.MaxResponseBytes = server.URL, server.Client(), cap
				return s.Fetch(ctx)
			},
		},
		{
			name: "lever",
			fetch: func() (any, error) {
				s := NewLever("acme", "Acme")
				s.BaseURL, s.HTTPClient, s.MaxResponseBytes = server.URL, server.Client(), cap
				return s.Fetch(ctx)
			},
		},
		{
			name: "ashby",
			fetch: func() (any, error) {
				s := NewAshby("acme", "Acme")
				s.BaseURL, s.HTTPClient, s.MaxResponseBytes = server.URL, server.Client(), cap
				return s.Fetch(ctx)
			},
		},
		{
			name: "smartrecruiters",
			fetch: func() (any, error) {
				s := NewSmartRecruiters("acme", "Acme")
				s.BaseURL, s.HTTPClient, s.MaxResponseBytes = server.URL, server.Client(), cap
				return s.Fetch(ctx)
			},
		},
		{
			name: "workday",
			fetch: func() (any, error) {
				host := strings.TrimPrefix(server.URL, "http://")
				s := NewWorkday("acme", host, "AcmeCareers", "Acme Inc")
				s.HTTPClient = &http.Client{Transport: rewriteHTTPSToHTTP{base: http.DefaultTransport}}
				s.MaxResponseBytes = cap
				return s.Fetch(ctx)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.fetch()
			if err == nil {
				t.Fatal("expected an oversized response to be rejected")
			}

			var tooLarge *httpbody.TooLargeError
			if !errors.As(err, &tooLarge) {
				t.Fatalf("expected a *httpbody.TooLargeError so a size failure is distinguishable from a decode failure, got %v", err)
			}
			if tooLarge.Limit != cap {
				t.Errorf("TooLargeError.Limit = %d, want %d", tooLarge.Limit, cap)
			}
			if !strings.Contains(err.Error(), "limit") {
				t.Errorf("expected the error to name the limit, got %v", err)
			}
		})
	}
}
