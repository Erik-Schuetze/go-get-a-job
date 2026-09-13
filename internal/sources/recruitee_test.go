package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recruiteeFixture mirrors the real shape of
// https://{company}.recruitee.com/api/offers/ (verified live against xneelo).
const recruiteeFixture = `{
  "offers": [
    {
      "id": 2730080,
      "title": "Platform Engineer",
      "city": "Leipzig",
      "country": "Germany",
      "location": "Leipzig, Germany",
      "department": "Engineering",
      "careers_url": "https://acme.recruitee.com/o/platform-engineer-12",
      "description": "<p>Own our <b>Crossplane</b> platform.</p>",
      "remote": true,
      "created_at": "2026-09-02 06:09:35 UTC"
    },
    {
      "id": 2730081,
      "title": "Account Executive",
      "city": "New York",
      "country": "United States",
      "department": "Sales",
      "careers_url": "https://acme.recruitee.com/o/account-executive",
      "description": "<p>Sell.</p>",
      "remote": false,
      "created_at": "2026-08-01 10:00:00 UTC"
    }
  ]
}`

func TestRecruitee_Fetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/offers" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(recruiteeFixture))
	}))
	defer server.Close()

	r := NewRecruitee("acme", "Acme Inc")
	r.BaseURL = server.URL

	jobs, err := r.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	first := jobs[0]
	if first.ID != "recruitee:acme:2730080" {
		t.Errorf("unexpected id %q", first.ID)
	}
	if first.URL != "https://acme.recruitee.com/o/platform-engineer-12" {
		t.Errorf("unexpected url %q", first.URL)
	}
	if first.PostedAt.IsZero() {
		t.Error("expected an parsed created_at")
	}
	if strings.Contains(first.Description, "<") {
		t.Errorf("description still contains markup: %q", first.Description)
	}
	// A remote posting is filed under an office city, so the remote marker has
	// to be part of the rendered location or a whitelist on "Germany" is the
	// only thing that can ever match it.
	if !strings.Contains(first.Location, "Remote") {
		t.Errorf("expected the remote marker in Location, got %q", first.Location)
	}
	if strings.Count(first.Location, "Germany") != 1 {
		t.Errorf("expected Germany exactly once (location and country overlap), got %q", first.Location)
	}

	if jobs[1].Location != "New York, United States" {
		t.Errorf("unexpected non-remote location %q", jobs[1].Location)
	}
}

func TestRecruitee_StatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := NewRecruitee("acme", "Acme Inc")
	r.BaseURL = server.URL

	if _, err := r.Fetch(context.Background()); err == nil {
		t.Fatal("expected an error for a 404")
	}
}
