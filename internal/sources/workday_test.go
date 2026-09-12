package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func workdayListPage(total int, postings ...workdayListPosting) string {
	b, _ := json.Marshal(workdayListResponse{Total: total, JobPostings: postings})
	return string(b)
}

func workdayDetailFixture(description, externalURL string) string {
	return fmt.Sprintf(`{"jobPostingInfo":{"jobDescription":%q,"externalUrl":%q}}`, description, externalURL)
}

// rewriteHTTPSToHTTP lets tests exercise the connector's real
// "https://<host>/..." URL construction against a plain-http
// httptest.Server, by downgrading the scheme right before the request is
// sent. This keeps the connector's production code free of test-only
// seams.
type rewriteHTTPSToHTTP struct {
	base http.RoundTripper
}

func (r rewriteHTTPSToHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	return r.base.RoundTrip(req)
}

func TestWorkday_Fetch_Paginates(t *testing.T) {
	var listBodies []string
	mux := http.NewServeMux()
	mux.HandleFunc("/wday/cxs/acme/AcmeCareers/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		listBodies = append(listBodies, string(body))

		var req workdayListRequest
		_ = json.Unmarshal(body, &req)

		switch req.Offset {
		case 0:
			_, _ = w.Write([]byte(workdayListPage(3,
				workdayListPosting{Title: "Platform Engineer", ExternalPath: "/job/US/Platform-Engineer_JR1", LocationsText: "US", BulletFields: []string{"JR1"}},
				workdayListPosting{Title: "DevOps Engineer", ExternalPath: "/job/DE/DevOps-Engineer_JR2", LocationsText: "Germany", BulletFields: []string{"JR2"}},
			)))
		case 2:
			_, _ = w.Write([]byte(workdayListPage(3,
				workdayListPosting{Title: "SRE", ExternalPath: "/job/DE/SRE_JR3", LocationsText: "Remote, Germany", BulletFields: []string{"JR3"}},
			)))
		default:
			t.Errorf("unexpected offset %d", req.Offset)
		}
	})
	mux.HandleFunc("/wday/cxs/acme/AcmeCareers/job/US/Platform-Engineer_JR1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(workdayDetailFixture("Own our Crossplane platform.", "https://acme.wd1.myworkdayjobs.com/AcmeCareers/job/US/Platform-Engineer_JR1")))
	})
	mux.HandleFunc("/wday/cxs/acme/AcmeCareers/job/DE/DevOps-Engineer_JR2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(workdayDetailFixture("Run our Terraform pipelines.", "https://acme.wd1.myworkdayjobs.com/AcmeCareers/job/DE/DevOps-Engineer_JR2")))
	})
	mux.HandleFunc("/wday/cxs/acme/AcmeCareers/job/DE/SRE_JR3", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(workdayDetailFixture("Keep things up.", "")))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	wd := NewWorkday("acme", host, "AcmeCareers", "Acme Inc")
	// The connector always builds "https://<Host>/...". Route those
	// requests to our plain-http test server by downgrading the scheme in
	// a custom RoundTripper, keeping the connector's production code free
	// of test-only seams.
	wd.HTTPClient = &http.Client{Transport: rewriteHTTPSToHTTP{base: http.DefaultTransport}}

	jobs, err := wd.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}

	if len(listBodies) != 2 {
		t.Fatalf("expected 2 list page requests, got %d", len(listBodies))
	}
	if !strings.Contains(listBodies[0], `"limit":20`) {
		t.Errorf("expected list request to use page size 20, got %q", listBodies[0])
	}

	if len(jobs) != 3 {
		t.Fatalf("expected 3 jobs across both pages, got %d", len(jobs))
	}

	byID := map[string]struct {
		title, location, url, description string
	}{}
	for _, j := range jobs {
		if j.Source != "workday" {
			t.Errorf("unexpected Source: %q", j.Source)
		}
		if j.Company != "Acme Inc" {
			t.Errorf("unexpected Company: %q", j.Company)
		}
		byID[j.ID] = struct{ title, location, url, description string }{j.Title, j.Location, j.URL, j.Description}
	}

	want1, ok := byID["workday:acme:JR1"]
	if !ok {
		t.Fatal("expected job workday:acme:JR1")
	}
	if want1.title != "Platform Engineer" || want1.location != "US" {
		t.Errorf("unexpected fields for JR1: %+v", want1)
	}
	if want1.description != "Own our Crossplane platform." {
		t.Errorf("unexpected description for JR1: %q", want1.description)
	}
	if want1.url != "https://acme.wd1.myworkdayjobs.com/AcmeCareers/job/US/Platform-Engineer_JR1" {
		t.Errorf("unexpected URL for JR1: %q", want1.url)
	}

	want3, ok := byID["workday:acme:JR3"]
	if !ok {
		t.Fatal("expected job workday:acme:JR3")
	}
	// JR3's detail fixture returns an empty externalUrl, so the connector
	// should fall back to constructing the URL from host/site/externalPath.
	if want3.url != fmt.Sprintf("https://%s/AcmeCareers/job/DE/SRE_JR3", host) {
		t.Errorf("unexpected fallback URL for JR3: %q", want3.url)
	}
}

func TestWorkday_Fetch_SkipsFailedDetail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/wday/cxs/acme/AcmeCareers/jobs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(workdayListPage(2,
			workdayListPosting{Title: "Good", ExternalPath: "/job/good", BulletFields: []string{"GOOD"}},
			workdayListPosting{Title: "Bad", ExternalPath: "/job/bad", BulletFields: []string{"BAD"}},
		)))
	})
	mux.HandleFunc("/wday/cxs/acme/AcmeCareers/job/good", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(workdayDetailFixture("All good.", "")))
	})
	mux.HandleFunc("/wday/cxs/acme/AcmeCareers/job/bad", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	wd := NewWorkday("acme", host, "AcmeCareers", "Acme Inc")
	wd.HTTPClient = &http.Client{Transport: rewriteHTTPSToHTTP{base: http.DefaultTransport}}

	jobs, err := wd.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job (the failing detail should be skipped), got %d", len(jobs))
	}
	if jobs[0].ID != "workday:acme:GOOD" {
		t.Errorf("expected the successful job to survive, got %q", jobs[0].ID)
	}
}

func TestWorkday_Fetch_ListHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	wd := NewWorkday("acme", host, "AcmeCareers", "Acme Inc")
	wd.HTTPClient = &http.Client{Transport: rewriteHTTPSToHTTP{base: http.DefaultTransport}}

	if _, err := wd.Fetch(context.Background()); err == nil {
		t.Fatal("expected error when list endpoint fails, got nil")
	}
}

func TestWorkday_RequisitionID_FallsBackToExternalPath(t *testing.T) {
	p := workdayListPosting{ExternalPath: "/job/foo"}
	if got := p.requisitionID(); got != "/job/foo" {
		t.Errorf("expected fallback to externalPath, got %q", got)
	}
}

func TestWorkday_Name(t *testing.T) {
	if (&Workday{}).Name() != "workday" {
		t.Error("expected Name() to return 'workday'")
	}
}
