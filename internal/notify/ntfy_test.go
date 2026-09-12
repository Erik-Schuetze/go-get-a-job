package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

type capturedPublish struct {
	path    string
	method  string
	headers http.Header
	body    string
}

func captureServer(t *testing.T, capture *capturedPublish, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.path = r.URL.Path
		capture.method = r.Method
		capture.headers = r.Header.Clone()
		capture.body = string(body)
		w.WriteHeader(status)
	}))
}

func TestNtfy_Notify(t *testing.T) {
	var captured capturedPublish
	server := captureServer(t, &captured, http.StatusOK)
	defer server.Close()

	n := NewNtfy(server.URL, "job-matches", "")
	job := model.Job{Company: "Acme Inc", Title: "Platform Engineer", Location: "Remote, Germany", URL: "https://example.com/job/1"}

	if err := n.Notify(context.Background(), job, "Mentions Crossplane and Terraform."); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if captured.method != http.MethodPost {
		t.Errorf("expected POST, got %s", captured.method)
	}
	if captured.path != "/job-matches" {
		t.Errorf("expected path /job-matches, got %q", captured.path)
	}
	if got := captured.headers.Get("Title"); got != "Acme Inc: Platform Engineer" {
		t.Errorf("unexpected Title header: %q", got)
	}
	if got := captured.headers.Get("Click"); got != "https://example.com/job/1" {
		t.Errorf("unexpected Click header: %q", got)
	}
	if got := captured.headers.Get("Priority"); got != "default" {
		t.Errorf("unexpected Priority header: %q", got)
	}
	if captured.headers.Get("Authorization") != "" {
		t.Errorf("expected no Authorization header when token is empty, got %q", captured.headers.Get("Authorization"))
	}
	if want := "Mentions Crossplane and Terraform.\nLocation: Remote, Germany"; captured.body != want {
		t.Errorf("unexpected body:\ngot:  %q\nwant: %q", captured.body, want)
	}
}

func TestNtfy_Notify_WithToken(t *testing.T) {
	var captured capturedPublish
	server := captureServer(t, &captured, http.StatusOK)
	defer server.Close()

	n := NewNtfy(server.URL, "job-matches", "secret-token")
	if err := n.Notify(context.Background(), model.Job{Title: "X", Company: "Y"}, "reason"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if got := captured.headers.Get("Authorization"); got != "Bearer secret-token" {
		t.Errorf("expected Bearer auth header, got %q", got)
	}
}

func TestNtfy_Notify_EmptyReasonUsesGenericMessage(t *testing.T) {
	var captured capturedPublish
	server := captureServer(t, &captured, http.StatusOK)
	defer server.Close()

	n := NewNtfy(server.URL, "job-matches", "")
	job := model.Job{Company: "Acme", Title: "SRE"}
	if err := n.Notify(context.Background(), job, ""); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if want := "New match: SRE at Acme"; captured.body != want {
		t.Errorf("unexpected body: %q, want %q", captured.body, want)
	}
}

func TestNtfy_Notify_NoURLNoClickHeader(t *testing.T) {
	var captured capturedPublish
	server := captureServer(t, &captured, http.StatusOK)
	defer server.Close()

	n := NewNtfy(server.URL, "job-matches", "")
	job := model.Job{Company: "Acme", Title: "SRE"} // no URL
	if err := n.Notify(context.Background(), job, "reason"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if captured.headers.Get("Click") != "" {
		t.Errorf("expected no Click header when job has no URL, got %q", captured.headers.Get("Click"))
	}
}

func TestNtfy_Notify_HTTPError(t *testing.T) {
	var captured capturedPublish
	server := captureServer(t, &captured, http.StatusForbidden)
	defer server.Close()

	n := NewNtfy(server.URL, "job-matches", "")
	if err := n.Notify(context.Background(), model.Job{Title: "X", Company: "Y"}, "reason"); err == nil {
		t.Fatal("expected error on 403 response, got nil")
	}
}

func TestNtfy_NotifyFailure(t *testing.T) {
	var captured capturedPublish
	server := captureServer(t, &captured, http.StatusOK)
	defer server.Close()

	n := NewNtfy(server.URL, "job-matches", "")
	if err := n.NotifyFailure(context.Background(), errors.New("boom")); err != nil {
		t.Fatalf("NotifyFailure returned error: %v", err)
	}

	if got := captured.headers.Get("Priority"); got != "high" {
		t.Errorf("expected high priority, got %q", got)
	}
	if got := captured.headers.Get("Title"); got != "go-get-a-job run failed" {
		t.Errorf("unexpected Title header: %q", got)
	}
	if captured.body != "boom" {
		t.Errorf("expected body 'boom', got %q", captured.body)
	}
}

func TestNtfy_URLTrimsTrailingSlash(t *testing.T) {
	var captured capturedPublish
	server := captureServer(t, &captured, http.StatusOK)
	defer server.Close()

	n := NewNtfy(server.URL+"/", "job-matches", "")
	if err := n.Notify(context.Background(), model.Job{Title: "X", Company: "Y"}, "reason"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if captured.path != "/job-matches" {
		t.Errorf("expected path /job-matches even with trailing slash in base URL, got %q", captured.path)
	}
}
