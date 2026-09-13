package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// personioFixture mirrors the real shape of a Personio XML feed (verified live
// against Stackable and Contabo). Job URLs are not in the feed at all; the
// connector derives them from the position ID.
const personioFixture = `<?xml version="1.0" encoding="UTF-8"?>
<workzag-jobs>
<position>
    <id>2576444</id>
    <subcompany>Acme GmbH</subcompany>
    <office>Remote (Germany)</office>
    <department>Infrastructure</department>
    <name>Platform Engineer</name>
    <jobDescriptions>
        <jobDescription>
            <name>Deine Aufgaben</name>
            <value><![CDATA[<p>Build <strong>Crossplane</strong> compositions.</p>]]></value>
        </jobDescription>
        <jobDescription>
            <name>Dein Profil</name>
            <value><![CDATA[<ul><li>Kubernetes</li></ul>]]></value>
        </jobDescription>
    </jobDescriptions>
    <schedule>full-time</schedule>
    <seniority>experienced</seniority>
    <createdAt>2026-03-25T14:54:34+00:00</createdAt>
</position>
<position>
    <id>999</id>
    <office>Berlin</office>
    <name>Brand Manager</name>
    <jobDescriptions></jobDescriptions>
    <createdAt>2026-09-07T08:00:00+00:00</createdAt>
</position>
</workzag-jobs>`

func TestPersonio_Fetch(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(personioFixture))
	}))
	defer server.Close()

	p := NewPersonio("acme", "Acme Inc")
	p.BaseURL = server.URL

	jobs, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if gotPath != "/xml" {
		t.Errorf("expected path /xml, got %q", gotPath)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	first := jobs[0]
	if first.ID != "personio:acme:2576444" {
		t.Errorf("unexpected id %q", first.ID)
	}
	if first.Company != "Acme Inc" || first.Source != "personio" {
		t.Errorf("unexpected company/source %q/%q", first.Company, first.Source)
	}
	// Description sections are labelled, because Personio's own headings carry
	// a lot of the meaning and a bare concatenation makes the sections run
	// together.
	if !strings.Contains(first.Description, "Deine Aufgaben") || !strings.Contains(first.Description, "Dein Profil") {
		t.Errorf("description lost its section names: %q", first.Description)
	}
	if strings.Contains(first.Description, "<") {
		t.Errorf("description still contains markup: %q", first.Description)
	}
	if first.PostedAt.IsZero() {
		t.Error("expected a parsed createdAt")
	}
	// An office of "Remote (Germany)" must survive verbatim so the location
	// pre-filter can match "Germany" inside it.
	if !strings.Contains(first.Location, "Germany") {
		t.Errorf("expected the office in Location, got %q", first.Location)
	}

	// The feed has no URL field; the derived one is what a notification links to.
	if !strings.HasSuffix(jobs[0].URL, "/job/2576444") {
		t.Errorf("expected a derived job URL, got %q", jobs[0].URL)
	}
}

// TestPersonio_WrongSubdomainIsExplained covers the one failure mode specific
// to Personio: an unknown company does not 404, it redirects to Personio's own
// login page, so the body is HTML and the XML parser would otherwise fail with
// an error that says nothing about the actual problem.
func TestPersonio_WrongSubdomainIsExplained(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Personio login</body></html>"))
	}))
	defer server.Close()

	p := NewPersonio("acme", "Acme Inc")
	p.BaseURL = server.URL

	_, err := p.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected an error for an HTML body")
	}
	if !strings.Contains(err.Error(), "subdomain") {
		t.Errorf("error should name the likely cause, got %v", err)
	}
}

func TestPersonio_StatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	p := NewPersonio("acme", "Acme Inc")
	p.BaseURL = server.URL

	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("expected an error for a 404")
	}
}
