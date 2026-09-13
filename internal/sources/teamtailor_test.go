package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// teamtailorFixture mirrors the real shape of
// https://{company}.teamtailor.com/jobs.json (verified live against Spacelift),
// which is a JSON Feed whose items embed a schema.org JobPosting.
const teamtailorFixture = `{
  "version": "https://jsonfeed.org/version/1.1",
  "title": "Acme jobs",
  "items": [
    {
      "id": "d800ea1f-a4f6-4d7e-a3d5-45a2a1f14614",
      "title": "Partner Account Manager (Remote Washington DC)",
      "url": "https://acme.teamtailor.com/jobs/8131815-partner-account-manager",
      "date_published": "2026-07-27T09:00:00.000+02:00",
      "content_html": "<p>Fallback copy.</p>",
      "_jobposting": {
        "title": "Partner Account Manager (Remote Washington DC)",
        "datePosted": "2026-07-27T07:00:00.000Z",
        "description": "<p>Own partner <b>enablement</b>.</p>",
        "jobLocation": [
          {"@type": "Place", "address": {
            "@type": "PostalAddress",
            "addressLocality": "Redwood City",
            "addressCountry": "US"
          }}
        ]
      }
    },
    {
      "id": "72137edc-b2b6-4a36-a2c7-7f77e75b4d36",
      "title": "Platform Engineer, Golang & IaC (Remote, European Union)",
      "url": "https://acme.teamtailor.com/jobs/7764743-platform-engineer",
      "date_published": "2026-05-20T09:00:00.000+02:00",
      "content_html": "<p>Fallback copy.</p>",
      "_jobposting": {
        "title": "Platform Engineer, Golang & IaC (Remote, European Union)",
        "datePosted": "2026-05-20T07:00:00.000Z",
        "description": "<p>Build <b>Crossplane</b> and Terraform tooling.</p>",
        "jobLocation": [
          {"@type": "Place", "address": {
            "@type": "PostalAddress",
            "addressLocality": "Warsaw",
            "addressCountry": "PL"
          }}
        ]
      }
    },
    {
      "id": "single-place-form",
      "title": "Site Reliability Engineer",
      "url": "https://acme.teamtailor.com/jobs/1-site-reliability-engineer",
      "date_published": "2026-06-01T09:00:00.000+02:00",
      "content_html": "<p>Keep the lights on.</p>",
      "_jobposting": {
        "title": "Site Reliability Engineer",
        "datePosted": "2026-06-01T07:00:00.000Z",
        "description": "",
        "jobLocation": {"@type": "Place", "address": {
          "@type": "PostalAddress",
          "addressLocality": "Leipzig",
          "addressCountry": "DE"
        }}
      }
    }
  ]
}`

func TestTeamtailor_Fetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jobs.json" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(teamtailorFixture))
	}))
	defer server.Close()

	tt := NewTeamtailor("acme", "Acme Inc")
	tt.BaseURL = server.URL

	jobs, err := tt.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(jobs))
	}

	if jobs[0].ID != "teamtailor:acme:d800ea1f-a4f6-4d7e-a3d5-45a2a1f14614" {
		t.Errorf("unexpected id %q", jobs[0].ID)
	}
	// datePosted (in the job posting) is preferred over date_published.
	if got := jobs[0].PostedAt.UTC().Format("15:04"); got != "07:00" {
		t.Errorf("expected datePosted 07:00 UTC, got %s", got)
	}
	if !strings.Contains(jobs[0].Description, "enablement") || strings.Contains(jobs[0].Description, "<") {
		t.Errorf("unexpected description %q", jobs[0].Description)
	}

	// The point of this connector's location handling: the only place this
	// posting says it can be done from is its title, and a Germany/EMEA
	// whitelist has to be able to see it.
	eu := jobs[1]
	if !strings.Contains(eu.Location, "European Union") {
		t.Errorf("expected the title's remote scope in Location, got %q", eu.Location)
	}
	if !strings.Contains(eu.Location, "Warsaw") {
		t.Errorf("expected the structured place to be kept too, got %q", eu.Location)
	}

	// schema.org allows a single Place instead of a list; that must not fail
	// the whole board.
	if jobs[2].Location != "Leipzig, DE" {
		t.Errorf("unexpected single-Place location %q", jobs[2].Location)
	}
	if !strings.Contains(jobs[2].Description, "Keep the lights on") {
		t.Errorf("expected a fallback to content_html, got %q", jobs[2].Description)
	}
}

func TestTeamtailorTitleScope(t *testing.T) {
	cases := []struct{ title, want string }{
		{"Platform Engineer, Golang & IaC (Remote, European Union)", "Remote, European Union"},
		{"Senior FP&A Analyst (Remote Poland)", "Remote Poland"},
		{"RVP Sales (Remote West Coast)", "Remote West Coast"},
		{"Site Reliability Engineer", ""},
		{"Data Engineer (Leipzig)", ""},
		{"Engineer (Berlin) (Remote, EU)", "Remote, EU"},
		{"", ""},
	}
	for _, c := range cases {
		if got := teamtailorTitleScope(c.title); got != c.want {
			t.Errorf("teamtailorTitleScope(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

func TestTeamtailor_StatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	tt := NewTeamtailor("acme", "Acme Inc")
	tt.BaseURL = server.URL

	_, err := tt.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected an error for a 404")
	}
	if !strings.Contains(err.Error(), fmt.Sprint(http.StatusNotFound)) {
		t.Errorf("error should include the status, got %v", err)
	}
}
