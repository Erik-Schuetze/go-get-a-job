package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ashbyFixture mirrors the real shape returned by
// https://api.ashbyhq.com/posting-api/job-board/{company} (verified against
// Notion's board, via GET, not POST).
const ashbyFixture = `{
  "jobs": [
    {
      "id": "job-1",
      "title": "Infrastructure Engineer",
      "department": "Engineering",
      "location": "Remote - Germany",
      "publishedAt": "2026-08-24T14:44:49.699+00:00",
      "isListed": true,
      "isRemote": true,
      "jobUrl": "https://jobs.ashbyhq.com/acme/job-1",
      "applyUrl": "https://jobs.ashbyhq.com/acme/job-1/application",
      "descriptionPlain": "Own our Crossplane-based IaC platform."
    },
    {
      "id": "job-2",
      "title": "Unlisted Role",
      "location": "Remote",
      "publishedAt": "2026-08-24T14:44:49.699+00:00",
      "isListed": false,
      "jobUrl": "https://jobs.ashbyhq.com/acme/job-2",
      "descriptionPlain": "Should be filtered out."
    }
  ]
}`

func TestAshby_Fetch(t *testing.T) {
	var gotMethod, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(ashbyFixture))
	}))
	defer server.Close()

	a := NewAshby("acme", "Acme Inc")
	a.BaseURL = server.URL

	jobs, err := a.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("expected GET request, got %s", gotMethod)
	}
	if gotPath != "/acme" {
		t.Errorf("expected path /acme, got %q", gotPath)
	}

	// job-2 is unlisted and should be filtered out.
	if len(jobs) != 1 {
		t.Fatalf("expected 1 listed job, got %d", len(jobs))
	}

	j := jobs[0]
	if j.ID != "ashby:acme:job-1" {
		t.Errorf("unexpected ID: %q", j.ID)
	}
	if j.Source != "ashby" {
		t.Errorf("unexpected Source: %q", j.Source)
	}
	if j.Title != "Infrastructure Engineer" {
		t.Errorf("unexpected Title: %q", j.Title)
	}
	if j.URL != "https://jobs.ashbyhq.com/acme/job-1" {
		t.Errorf("unexpected URL: %q", j.URL)
	}
	if j.Description != "Own our Crossplane-based IaC platform." {
		t.Errorf("unexpected Description: %q", j.Description)
	}
	if j.PostedAt.IsZero() {
		t.Error("expected non-zero PostedAt")
	}
}

func TestAshby_Fetch_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Unauthorized"))
	}))
	defer server.Close()

	a := NewAshby("acme", "Acme Inc")
	a.BaseURL = server.URL

	if _, err := a.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on 401 response, got nil")
	}
}

func TestAshby_Name(t *testing.T) {
	if (&Ashby{}).Name() != "ashby" {
		t.Error("expected Name() to return 'ashby'")
	}
}
