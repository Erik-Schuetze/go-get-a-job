package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// leverFixture mirrors the real shape returned by
// https://api.lever.co/v0/postings/{company}?mode=json (verified live
// during planning against Spotify's board): a top-level JSON array.
const leverFixture = `[
  {
    "id": "abc-123",
    "text": "Platform Engineer",
    "categories": {"location": "Berlin, Germany", "department": "Engineering"},
    "createdAt": 1700000000000,
    "descriptionPlain": "Build our Terraform-based platform.",
    "hostedUrl": "https://jobs.lever.co/acme/abc-123",
    "applyUrl": "https://jobs.lever.co/acme/abc-123/apply"
  }
]`

func TestLever_Fetch(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(leverFixture))
	}))
	defer server.Close()

	l := NewLever("acme", "Acme Inc")
	l.BaseURL = server.URL

	jobs, err := l.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if gotPath != "/acme" {
		t.Errorf("expected path /acme, got %q", gotPath)
	}
	if gotQuery != "mode=json" {
		t.Errorf("expected query mode=json, got %q", gotQuery)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	j := jobs[0]
	if j.ID != "lever:acme:abc-123" {
		t.Errorf("unexpected ID: %q", j.ID)
	}
	if j.Source != "lever" {
		t.Errorf("unexpected Source: %q", j.Source)
	}
	if j.Title != "Platform Engineer" {
		t.Errorf("unexpected Title: %q", j.Title)
	}
	if j.Location != "Berlin, Germany" {
		t.Errorf("unexpected Location: %q", j.Location)
	}
	if j.URL != "https://jobs.lever.co/acme/abc-123" {
		t.Errorf("unexpected URL: %q", j.URL)
	}
	if j.Description != "Build our Terraform-based platform." {
		t.Errorf("unexpected Description: %q", j.Description)
	}
	if j.PostedAt.Unix() != 1700000000 {
		t.Errorf("unexpected PostedAt: %v", j.PostedAt)
	}
}

func TestLever_Fetch_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error":"Document not found"}`))
	}))
	defer server.Close()

	l := NewLever("nonexistent", "Nonexistent Inc")
	l.BaseURL = server.URL

	if _, err := l.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on 404 response, got nil")
	}
}

func TestLever_Fetch_EmptyBoard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	l := NewLever("acme", "Acme Inc")
	l.BaseURL = server.URL

	jobs, err := l.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs, got %d", len(jobs))
	}
}

func TestLever_Name(t *testing.T) {
	if (&Lever{}).Name() != "lever" {
		t.Error("expected Name() to return 'lever'")
	}
}
