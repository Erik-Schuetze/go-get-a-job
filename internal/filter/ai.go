package filter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// AIScore is the model's judgment of how well a job matches a user's
// profile.
type AIScore struct {
	// Score is 0-1, where 1 is a perfect match.
	Score float64 `json:"score"`
	// Reason is a short, human-readable explanation, suitable for
	// inclusion directly in a notification.
	Reason string `json:"reason"`
	// Signals lists the specific terms/phrases from the posting that
	// drove the score (e.g. "Crossplane", "Terraform", "platform team").
	Signals []string `json:"signals"`
}

// Scorer judges how well a job matches a free-text profile. It's an
// interface so the runner can be tested against a fake instead of making
// real, billed API calls.
type Scorer interface {
	Score(ctx context.Context, job model.Job, profile string) (AIScore, error)
}

// AIScorer calls an OpenAI-compatible chat completions API (DeepSeek by
// default, but any compatible provider works) to judge job relevance.
type AIScorer struct {
	BaseURL string
	APIKey  string
	Model   string

	HTTPClient *http.Client
}

// NewAIScorer builds an AIScorer for the given provider base URL, API key,
// and model identifier.
func NewAIScorer(baseURL, apiKey, model string) *AIScorer {
	return &AIScorer{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// maxDescriptionChars bounds how much of a job's description is sent to
// the model, to keep prompts (and cost) reasonable even for very long
// postings.
const maxDescriptionChars = 6000

const systemPrompt = `You are a job-relevance classifier for a job search assistant.
Given a candidate's profile (what they're looking for) and a single job
posting, decide how well the posting matches. Respond with ONLY a JSON
object (no markdown fences, no commentary) with exactly these fields:

{
  "score": <number between 0 and 1, where 1 is a perfect match>,
  "reason": "<one or two sentences, written to the candidate, explaining the score, e.g. 'Mentions Crossplane and Terraform for a platform team.'>",
  "signals": ["<short phrase>", "..."]
}

Be conservative: a generic DevOps/SRE/Platform posting with no clear
alignment to the profile's specific interests should score around 0.4-0.6,
not high. Reserve scores above 0.8 for postings that clearly match both the
general role type AND the specific technologies/interests called out in
the profile. If the posting is clearly unrelated to the profile (e.g. a
sales or marketing role), score it near 0.`

type chatRequest struct {
	Model          string              `json:"model"`
	Messages       []chatMessage       `json:"messages"`
	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`
	Temperature    float64             `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Score sends job and profile to the configured model and returns its
// relevance judgment.
func (s *AIScorer) Score(ctx context.Context, job model.Job, profile string) (AIScore, error) {
	userContent := fmt.Sprintf(
		"Candidate profile:\n%s\n\nJob posting:\nCompany: %s\nTitle: %s\nLocation: %s\nDescription:\n%s",
		strings.TrimSpace(profile), job.Company, job.Title, job.Location, truncate(job.Description, maxDescriptionChars),
	)

	reqBody := chatRequest{
		Model: s.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
		ResponseFormat: &chatResponseFormat{Type: "json_object"},
		Temperature:    0.2,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return AIScore{}, fmt.Errorf("encoding AI request: %w", err)
	}

	endpoint := strings.TrimRight(s.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return AIScore{}, fmt.Errorf("building AI request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.APIKey)

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return AIScore{}, fmt.Errorf("AI request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return AIScore{}, fmt.Errorf("AI request: unexpected status %d: %s", resp.StatusCode, string(errBody))
	}

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return AIScore{}, fmt.Errorf("decoding AI response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return AIScore{}, fmt.Errorf("AI response contained no choices")
	}

	var score AIScore
	if err := json.Unmarshal([]byte(parsed.Choices[0].Message.Content), &score); err != nil {
		return AIScore{}, fmt.Errorf("parsing AI score JSON %q: %w", parsed.Choices[0].Message.Content, err)
	}

	score.Score = clamp01(score.Score)
	return score, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "... [truncated]"
}

func clamp01(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	default:
		return f
	}
}
