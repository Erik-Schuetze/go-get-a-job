package sources

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// TestLabels_AreUniquePerConfiguredSource is the property the dead-source
// guard depends on. Name() is the connector type, shared by every board of
// that type, so a guard keyed on it would merge the histories of all six
// Greenhouse boards in a real config - and five healthy boards would silently
// cover for a sixth that had been dead for a month.
func TestLabels_AreUniquePerConfiguredSource(t *testing.T) {
	cfgs := []config.SourceConfig{
		{Type: "greenhouse", Company: "grafanalabs", DisplayName: "Grafana Labs"},
		{Type: "greenhouse", Company: "gitlab", DisplayName: "GitLab"},
		{Type: "lever", Company: "acme", DisplayName: "Acme"},
		{Type: "ashby", Company: "acme", DisplayName: "Acme"},
		{Type: "smartrecruiters", Company: "acme", DisplayName: "Acme"},
		{Type: "workday", Tenant: "acme", Host: "acme.wd1.myworkdayjobs.com", Site: "Acme", DisplayName: "Acme"},
		{Type: "workday", Tenant: "acme", Host: "acme.wd1.myworkdayjobs.com", Site: "AcmeCareers", DisplayName: "Acme Careers"},
	}

	built, err := BuildAll(cfgs)
	if err != nil {
		t.Fatalf("BuildAll returned error: %v", err)
	}

	seen := map[string]bool{}
	for _, s := range built {
		label := s.Label()
		if label == "" {
			t.Errorf("%s source has an empty Label", s.Name())
			continue
		}
		if seen[label] {
			t.Errorf("duplicate label %q: two configured sources would share one history", label)
		}
		seen[label] = true

		if !strings.HasPrefix(label, s.Name()+"/") {
			t.Errorf("Label %q should start with the connector name %q", label, s.Name())
		}
	}

	if !seen["greenhouse/grafanalabs"] {
		t.Errorf("expected a greenhouse/grafanalabs label, got %v", seen)
	}
	if !seen["workday/acme/AcmeCareers"] {
		t.Errorf("expected a site-qualified Workday label, got %v", seen)
	}
}

// TestDefaultHTTPClient_IsShared locks in that every connector shares one
// client. A client per source would make both guard limits per-board: the
// pacing floor would be multiplied by the number of sources, and the request
// budget would be multiplied with it, so `maxRequestsPerRun` would quietly
// stop being a per-run ceiling on exactly the configs that need one.
func TestDefaultHTTPClient_IsShared(t *testing.T) {
	a := NewGreenhouse("acme", "Acme")
	b := NewLever("acme", "Acme")
	if a.HTTPClient != b.HTTPClient {
		t.Error("expected every source to share one outbound client")
	}
	if a.HTTPClient == http.DefaultClient {
		t.Error("expected the shared client to be the paced one, not http.DefaultClient")
	}
}

// TestConfigureFromConfig guards against a limit that silently wraps: a
// time.Duration holds nanoseconds, so a large millisecond figure is meant
// either to slow the scraper down or to fail loudly, never to become "no delay
// at all".
func TestConfigureFromConfig(t *testing.T) {
	if err := ConfigureFromConfig(config.Config{Guard: config.GuardConfig{
		MinRequestIntervalMs: 2_000_000_000,
	}}); err == nil {
		t.Error("expected an error for an implausible interval, got nil")
	}

	if err := ConfigureFromConfig(config.Config{Guard: config.GuardConfig{
		MinRequestIntervalMs: 500,
		MaxRequestsPerRun:    100,
	}}); err != nil {
		t.Errorf("ConfigureFromConfig returned error for sane values: %v", err)
	}
}

// TestValidate_ReportsEachFailureMode covers the content problems a fetch
// error cannot express, plus the fatal-error path.
func TestValidate_ReportsEachFailureMode(t *testing.T) {
	t.Run("a board that answers with nothing", func(t *testing.T) {
		// The case the whole mode exists for: SmartRecruiters answers
		// 200 + totalFound:0 for any slug at all, so "no postings" is the only
		// available signal that a board identifier is wrong.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jobs":[]}`))
		}))
		defer server.Close()

		gh := NewGreenhouse("empty", "Empty Inc")
		gh.BaseURL = server.URL

		out := validate(t, gh)
		if !strings.Contains(out, "greenhouse/empty") {
			t.Errorf("log should name the unhealthy source, got %q", out)
		}
	})

	t.Run("postings missing required fields", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Parses and yields a row, but one nothing downstream can act on:
			// no title to notify with. Only a content check catches this.
			_, _ = w.Write([]byte(`{"jobs":[{"id":1,"title":"","location":{"name":"Berlin"}}]}`))
		}))
		defer server.Close()

		gh := NewGreenhouse("untitled", "Untitled Inc")
		gh.BaseURL = server.URL

		out := validate(t, gh)
		if !strings.Contains(out, "greenhouse/untitled") {
			t.Errorf("log should name the unhealthy source, got %q", out)
		}
	})

	t.Run("a fetch that errors", func(t *testing.T) {
		out := validate(t, errSource{err: errors.New("connection refused")})
		if !strings.Contains(out, "boom/acme") {
			t.Errorf("log should name the failing source, got %q", out)
		}
	})
}

// TestValidate_CancelledContextIsInconclusive covers the one case where
// reporting "unhealthy" would be a lie: the run was cut short, so nothing was
// learned about the sources that were never fetched.
func TestValidate_CancelledContextIsInconclusive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := validateCtx(t, ctx, errSource{err: context.Canceled})
	if !strings.Contains(out, "inconclusive") {
		t.Errorf("a cancelled run should be reported as inconclusive, got %q", out)
	}
}

// validate runs Validate over one source and returns whatever it logged.
func validate(t *testing.T, srcs ...Source) string {
	t.Helper()
	return validateCtx(t, context.Background(), srcs...)
}

func validateCtx(t *testing.T, ctx context.Context, srcs ...Source) string {
	t.Helper()
	var out strings.Builder
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if healthy := Validate(ctx, logger, srcs); healthy {
		t.Error("expected Validate to report the sources as unhealthy")
	}
	return out.String()
}

// errSource is a Source whose Fetch always fails.
type errSource struct{ err error }

func (e errSource) Name() string  { return "boom" }
func (e errSource) Label() string { return "boom/acme" }
func (e errSource) Fetch(context.Context) ([]model.Job, error) {
	return nil, e.err
}
