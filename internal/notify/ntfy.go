package notify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// Ntfy delivers notifications via an ntfy (https://ntfy.sh, or
// self-hosted) topic: https://docs.ntfy.sh/publish/
type Ntfy struct {
	// URL is the base server URL, e.g. "https://ntfy.example.com" or
	// "http://ntfy.go-get-a-job.svc.cluster.local".
	URL   string
	Topic string
	// Token, if set, is sent as a Bearer token - required if the ntfy
	// server/topic is configured to deny anonymous publishing.
	Token string

	HTTPClient *http.Client
}

// NewNtfy builds an Ntfy notifier for the given server URL, topic, and
// optional auth token (pass "" if the topic doesn't require auth).
func NewNtfy(url, topic, token string) *Ntfy {
	return &Ntfy{
		URL:        strings.TrimRight(url, "/"),
		Topic:      topic,
		Token:      token,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Notify publishes a single job match. reason is the AI scorer's
// human-readable explanation; if empty, a generic message is used
// instead.
func (n *Ntfy) Notify(ctx context.Context, job model.Job, reason string) error {
	body := reason
	if body == "" {
		body = fmt.Sprintf("New match: %s at %s", job.Title, job.Company)
	}
	if job.Location != "" {
		body = fmt.Sprintf("%s\nLocation: %s", body, job.Location)
	}

	headers := map[string]string{
		"Title":    fmt.Sprintf("%s: %s", job.Company, job.Title),
		"Priority": "default",
		"Tags":     "briefcase",
	}
	if job.URL != "" {
		headers["Click"] = job.URL
	}

	return n.publish(ctx, body, headers)
}

// NotifyFailure publishes a high-priority alert that a run failed.
func (n *Ntfy) NotifyFailure(ctx context.Context, runErr error) error {
	headers := map[string]string{
		"Title":    "go-get-a-job run failed",
		"Priority": "high",
		"Tags":     "warning",
	}
	return n.publish(ctx, runErr.Error(), headers)
}

func (n *Ntfy) publish(ctx context.Context, body string, headers map[string]string) error {
	url := fmt.Sprintf("%s/%s", n.URL, n.Topic)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		return fmt.Errorf("ntfy: building request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}

	resp, err := n.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("ntfy: unexpected status %d: %s", resp.StatusCode, string(errBody))
	}
	return nil
}

var _ Notifier = (*Ntfy)(nil)
