package filter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// fakeChatServer starts an httptest server that mimics an OpenAI-compatible
// /chat/completions endpoint, replying with the given assistant message
// content (itself expected to be a JSON-encoded AIScore, mirroring how
// DeepSeek's JSON mode behaves).
func fakeChatServer(t *testing.T, assistantContent string, capture *capturedRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			body, _ := io.ReadAll(r.Body)
			capture.path = r.URL.Path
			capture.authHeader = r.Header.Get("Authorization")
			capture.body = string(body)
		}
		resp := chatResponse{
			Choices: []struct {
				Message chatMessage `json:"message"`
			}{
				{Message: chatMessage{Role: "assistant", Content: assistantContent}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

type capturedRequest struct {
	path       string
	authHeader string
	body       string
}

func TestAIScorer_Score(t *testing.T) {
	var captured capturedRequest
	server := fakeChatServer(t, `{"score": 0.9, "reason": "Strong Crossplane match.", "signals": ["Crossplane", "Terraform"]}`, &captured)
	defer server.Close()

	scorer := NewAIScorer(server.URL, "test-api-key", "deepseek-chat")
	job := model.Job{Company: "Acme", Title: "Platform Engineer", Location: "Remote", Description: "Own our Crossplane platform."}

	score, err := scorer.Score(context.Background(), job, "Looking for Crossplane/IaC roles.")
	if err != nil {
		t.Fatalf("Score returned error: %v", err)
	}

	if score.Score != 0.9 {
		t.Errorf("expected score 0.9, got %v", score.Score)
	}
	if score.Reason != "Strong Crossplane match." {
		t.Errorf("unexpected reason: %q", score.Reason)
	}
	if len(score.Signals) != 2 {
		t.Errorf("expected 2 signals, got %d", len(score.Signals))
	}

	if captured.path != "/chat/completions" {
		t.Errorf("expected path /chat/completions, got %q", captured.path)
	}
	if captured.authHeader != "Bearer test-api-key" {
		t.Errorf("expected Bearer auth header, got %q", captured.authHeader)
	}
	if !strings.Contains(captured.body, "Crossplane platform") {
		t.Errorf("expected request body to include job description, got %q", captured.body)
	}
	if !strings.Contains(captured.body, "Looking for Crossplane/IaC roles.") {
		t.Errorf("expected request body to include profile text, got %q", captured.body)
	}
	if !strings.Contains(captured.body, `"type":"json_object"`) {
		t.Errorf("expected request to ask for JSON mode, got %q", captured.body)
	}
}

func TestAIScorer_Score_ClampsOutOfRange(t *testing.T) {
	server := fakeChatServer(t, `{"score": 1.5, "reason": "over", "signals": []}`, nil)
	defer server.Close()

	scorer := NewAIScorer(server.URL, "key", "deepseek-chat")
	score, err := scorer.Score(context.Background(), model.Job{}, "profile")
	if err != nil {
		t.Fatalf("Score returned error: %v", err)
	}
	if score.Score != 1 {
		t.Errorf("expected score clamped to 1, got %v", score.Score)
	}
}

func TestAIScorer_Score_NegativeClamped(t *testing.T) {
	server := fakeChatServer(t, `{"score": -0.3, "reason": "under", "signals": []}`, nil)
	defer server.Close()

	scorer := NewAIScorer(server.URL, "key", "deepseek-chat")
	score, err := scorer.Score(context.Background(), model.Job{}, "profile")
	if err != nil {
		t.Fatalf("Score returned error: %v", err)
	}
	if score.Score != 0 {
		t.Errorf("expected score clamped to 0, got %v", score.Score)
	}
}

func TestAIScorer_Score_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer server.Close()

	scorer := NewAIScorer(server.URL, "bad-key", "deepseek-chat")
	if _, err := scorer.Score(context.Background(), model.Job{}, "profile"); err == nil {
		t.Fatal("expected error on 401 response, got nil")
	}
}

func TestAIScorer_Score_NoChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices": []}`))
	}))
	defer server.Close()

	scorer := NewAIScorer(server.URL, "key", "deepseek-chat")
	if _, err := scorer.Score(context.Background(), model.Job{}, "profile"); err == nil {
		t.Fatal("expected error when response has no choices, got nil")
	}
}

func TestAIScorer_Score_InvalidJSONContent(t *testing.T) {
	server := fakeChatServer(t, "not valid json", nil)
	defer server.Close()

	scorer := NewAIScorer(server.URL, "key", "deepseek-chat")
	if _, err := scorer.Score(context.Background(), model.Job{}, "profile"); err == nil {
		t.Fatal("expected error when assistant content isn't valid JSON, got nil")
	}
}
