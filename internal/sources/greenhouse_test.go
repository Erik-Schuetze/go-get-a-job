package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// greenhouseFixture mirrors the real shape returned by
// https://boards-api.greenhouse.io/v1/boards/{company}/jobs?content=true
// (verified live against Grafana Labs' board during planning).
const greenhouseFixture = `{
  "jobs": [
    {
      "absolute_url": "https://job-boards.greenhouse.io/acme/jobs/123",
      "id": 123,
      "updated_at": "2026-08-18T09:53:18-04:00",
      "requisition_id": "27164",
      "title": "Platform Engineer - Crossplane",
      "location": {"name": "Remote (Germany)"},
      "content": "<p>We build <b>Crossplane</b> compositions.</p><p>Apply now!</p>",
      "first_published": "2026-05-19T08:17:09-04:00"
    },
    {
      "absolute_url": "https://job-boards.greenhouse.io/acme/jobs/456",
      "id": 456,
      "updated_at": "2026-08-18T09:53:25-04:00",
      "requisition_id": "27165",
      "title": "Sales Manager",
      "location": {"name": "New York"},
      "content": "<p>Sell things.</p>",
      "first_published": "2026-05-19T08:14:06-04:00"
    }
  ]
}`

func TestGreenhouse_Fetch(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Query().Get("content") != "true" {
			t.Errorf("expected content=true query param, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(greenhouseFixture))
	}))
	defer server.Close()

	gh := NewGreenhouse("acme", "Acme Inc")
	gh.BaseURL = server.URL

	jobs, err := gh.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if gotPath != "/acme/jobs" {
		t.Errorf("expected path /acme/jobs, got %q", gotPath)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	first := jobs[0]
	if first.ID != "greenhouse:acme:123" {
		t.Errorf("unexpected ID: %q", first.ID)
	}
	if first.Source != "greenhouse" {
		t.Errorf("unexpected Source: %q", first.Source)
	}
	if first.Company != "Acme Inc" {
		t.Errorf("unexpected Company: %q", first.Company)
	}
	if first.Title != "Platform Engineer - Crossplane" {
		t.Errorf("unexpected Title: %q", first.Title)
	}
	if first.Location != "Remote (Germany)" {
		t.Errorf("unexpected Location: %q", first.Location)
	}
	if first.URL != "https://job-boards.greenhouse.io/acme/jobs/123" {
		t.Errorf("unexpected URL: %q", first.URL)
	}
	if want := "We build Crossplane compositions.\nApply now!"; first.Description != want {
		t.Errorf("unexpected Description:\ngot:  %q\nwant: %q", first.Description, want)
	}
	if first.PostedAt.IsZero() {
		t.Error("expected non-zero PostedAt")
	}
}

func TestGreenhouse_Fetch_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	gh := NewGreenhouse("acme", "Acme Inc")
	gh.BaseURL = server.URL

	if _, err := gh.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on non-200 response, got nil")
	}
}

func TestGreenhouse_Fetch_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	gh := NewGreenhouse("acme", "Acme Inc")
	gh.BaseURL = server.URL

	if _, err := gh.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on invalid JSON, got nil")
	}
}

func TestGreenhouse_Name(t *testing.T) {
	if (&Greenhouse{}).Name() != "greenhouse" {
		t.Error("expected Name() to return 'greenhouse'")
	}
}
