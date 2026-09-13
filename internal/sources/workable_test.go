package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// workableFixture mirrors the real shape of
// https://apply.workable.com/api/v1/widget/accounts/{company}?details=true
// (verified live against Hugging Face).
const workableFixture = `{
  "name": "Acme",
  "description": null,
  "jobs": [
    {
      "shortcode": "F4C096B22E",
      "code": "12345",
      "title": "Low-level Senior Software Engineer, Storage - EMEA Remote",
      "city": "Paris",
      "state": "Ile-de-France",
      "country": "France",
      "telecommuting": true,
      "url": "https://apply.workable.com/j/F4C096B22E",
      "shortlink": "https://apply.workable.com/j/F4C096B22E",
      "department": "Engineering",
      "description": "<p>Work on <b>storage</b>.</p>",
      "requirements": "<ul><li>Go, Rust, Kubernetes</li></ul>",
      "employment_type": "Full-time",
      "created_at": "2026-05-29",
      "published_on": "2026-07-30"
    },
    {
      "shortcode": "",
      "code": "98765",
      "title": "Office Manager",
      "city": "Berlin",
      "country": "Germany",
      "telecommuting": false,
      "shortlink": "https://apply.workable.com/j/ABC",
      "description": "",
      "created_at": "2026-06-01T09:00:00Z",
      "published_on": ""
    }
  ]
}`

func TestWorkable_Fetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/acme" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		// Without details=true the API omits descriptions entirely, which
		// would leave the scorer with a title to judge.
		if r.URL.Query().Get("details") != "true" {
			t.Errorf("expected details=true, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(workableFixture))
	}))
	defer server.Close()

	w := NewWorkable("acme", "Acme Inc")
	w.BaseURL = server.URL

	jobs, err := w.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	first := jobs[0]
	if first.ID != "workable:acme:F4C096B22E" {
		t.Errorf("unexpected id %q", first.ID)
	}
	if first.Location != "Remote, Paris, Ile-de-France, France" {
		t.Errorf("unexpected location %q", first.Location)
	}
	if got := first.PostedAt.Format("2006-01-02"); got != "2026-07-30" {
		t.Errorf("expected published_on to win, got %s", got)
	}
	// Requirements carry the stack, so dropping them would mean scoring the
	// intro paragraph alone.
	if !strings.Contains(first.Description, "Requirements") || !strings.Contains(first.Description, "Kubernetes") {
		t.Errorf("expected requirements in the description, got %q", first.Description)
	}

	// A window without a shortcode falls back to the numeric code, so an older
	// board still dedups instead of silently producing duplicate alerts.
	if jobs[1].ID != "workable:acme:98765" {
		t.Errorf("expected a fallback to code, got %q", jobs[1].ID)
	}
	if jobs[1].URL != "https://apply.workable.com/j/ABC" {
		t.Errorf("expected a fallback to shortlink, got %q", jobs[1].URL)
	}
	if jobs[1].Location != "Berlin, Germany" {
		t.Errorf("unexpected non-remote location %q", jobs[1].Location)
	}
}

// TestWorkable_EmptyBoard covers the ambiguity that makes Workable different
// from the other connectors: a slightly wrong slug can answer 200 with an
// existing but empty account rather than 404ing, so an empty result is a
// success, not an error, and the dead-source guard is what notices it later.
func TestWorkable_EmptyBoard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"Grafana","description":null,"jobs":[]}`))
	}))
	defer server.Close()

	wk := NewWorkable("grafana", "Grafana")
	wk.BaseURL = server.URL

	jobs, err := wk.Fetch(context.Background())
	if err != nil {
		t.Fatalf("an empty board is not an error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs, got %d", len(jobs))
	}
}

func TestWorkable_StatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	wk := NewWorkable("acme", "Acme Inc")
	wk.BaseURL = server.URL

	if _, err := wk.Fetch(context.Background()); err == nil {
		t.Fatal("expected an error for a 404")
	}
}
