package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// smartRecruitersListPage mirrors the real shape returned by
// GET /v1/companies/{company}/postings (verified live during planning).
func smartRecruitersListPage(offset, totalFound int, ids ...string) string {
	var items []string
	for _, id := range ids {
		items = append(items, fmt.Sprintf(`{
			"id": %q, "name": "Platform Engineer %s", "releasedDate": "2026-09-09T09:43:26.403Z",
			"location": {"fullLocation": "Berlin, Germany"}
		}`, id, id))
	}
	return fmt.Sprintf(`{"offset":%d,"limit":2,"totalFound":%d,"content":[%s]}`, offset, totalFound, strings.Join(items, ","))
}

// smartRecruitersDetailFixture mirrors the real shape returned by
// GET /v1/companies/{company}/postings/{id} (verified live during
// planning).
func smartRecruitersDetailFixture(id string) string {
	return fmt.Sprintf(`{
		"id": %q,
		"postingUrl": "https://jobs.smartrecruiters.com/acme/%s",
		"jobAd": {
			"sections": {
				"jobDescription": {"title": "Job Description", "text": "<p>Own our Crossplane platform.</p>"},
				"qualifications": {"title": "Qualifications", "text": "<p>Terraform experience required.</p>"}
			}
		}
	}`, id, id)
}

func TestSmartRecruiters_Fetch_Paginates(t *testing.T) {
	var listCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/acme/postings", func(w http.ResponseWriter, r *http.Request) {
		listCalls++
		offset := r.URL.Query().Get("offset")
		switch offset {
		case "0":
			_, _ = w.Write([]byte(smartRecruitersListPage(0, 3, "1", "2")))
		case "2":
			_, _ = w.Write([]byte(smartRecruitersListPage(2, 3, "3")))
		default:
			t.Errorf("unexpected offset %q", offset)
		}
	})
	mux.HandleFunc("/acme/postings/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(smartRecruitersDetailFixture("1")))
	})
	mux.HandleFunc("/acme/postings/2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(smartRecruitersDetailFixture("2")))
	})
	mux.HandleFunc("/acme/postings/3", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(smartRecruitersDetailFixture("3")))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	sr := NewSmartRecruiters("acme", "Acme Inc")
	sr.BaseURL = server.URL

	jobs, err := sr.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if listCalls != 2 {
		t.Errorf("expected 2 list page requests, got %d", listCalls)
	}
	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs across both pages, got %d", len(jobs))
	}

	byID := map[string]bool{}
	for _, j := range jobs {
		byID[j.ID] = true
		if j.Source != "smartrecruiters" {
			t.Errorf("unexpected Source: %q", j.Source)
		}
		if j.Company != "Acme Inc" {
			t.Errorf("unexpected Company: %q", j.Company)
		}
		if j.Location != "Berlin, Germany" {
			t.Errorf("unexpected Location: %q", j.Location)
		}
		if !strings.Contains(j.Description, "Crossplane") || !strings.Contains(j.Description, "Terraform") {
			t.Errorf("expected description to contain both sections, got %q", j.Description)
		}
		if !strings.HasPrefix(j.URL, "https://jobs.smartrecruiters.com/acme/") {
			t.Errorf("unexpected URL: %q", j.URL)
		}
	}
	for _, id := range []string{"smartrecruiters:acme:1", "smartrecruiters:acme:2", "smartrecruiters:acme:3"} {
		if !byID[id] {
			t.Errorf("expected job with ID %q", id)
		}
	}
}

func TestSmartRecruiters_Fetch_SkipsFailedDetail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/acme/postings", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(smartRecruitersListPage(0, 2, "good", "bad")))
	})
	mux.HandleFunc("/acme/postings/good", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(smartRecruitersDetailFixture("good")))
	})
	mux.HandleFunc("/acme/postings/bad", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	sr := NewSmartRecruiters("acme", "Acme Inc")
	sr.BaseURL = server.URL

	jobs, err := sr.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job (the failing detail should be skipped), got %d", len(jobs))
	}
	if jobs[0].ID != "smartrecruiters:acme:good" {
		t.Errorf("expected the successful job to survive, got %q", jobs[0].ID)
	}
}

func TestSmartRecruiters_Fetch_ListHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	sr := NewSmartRecruiters("acme", "Acme Inc")
	sr.BaseURL = server.URL

	if _, err := sr.Fetch(context.Background()); err == nil {
		t.Fatal("expected error when list endpoint fails, got nil")
	}
}

func TestSmartRecruiters_Fetch_EmptyBoard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"offset":0,"limit":100,"totalFound":0,"content":[]}`))
	}))
	defer server.Close()

	sr := NewSmartRecruiters("acme", "Acme Inc")
	sr.BaseURL = server.URL

	jobs, err := sr.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs, got %d", len(jobs))
	}
}

func TestSmartRecruiters_Name(t *testing.T) {
	if (&SmartRecruiters{}).Name() != "smartrecruiters" {
		t.Error("expected Name() to return 'smartrecruiters'")
	}
}
