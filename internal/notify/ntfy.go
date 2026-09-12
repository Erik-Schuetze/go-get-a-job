package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpbody"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
	"github.com/Erik-Schuetze/go-get-a-job/internal/sanitize"
)

// Bounds on the notification text. Everything below originates in a
// third-party API response or in the LLM's reply, and it lands on the
// operator's phone, so an oversized or hostile value must not be able to
// flood the notification or produce an invalid HTTP header value.
const (
	maxTitleChars  = 120
	maxReasonChars = 500
	maxBodyChars   = 1000
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
	// MaxErrorBytes caps how much of an error response body is included in
	// the returned error; see internal/httpbody.
	MaxErrorBytes int64
}

// NewNtfy builds an Ntfy notifier for the given server URL, topic, and
// optional auth token (pass "" if the topic doesn't require auth).
func NewNtfy(url, topic, token string) *Ntfy {
	return &Ntfy{
		URL:           strings.TrimRight(url, "/"),
		Topic:         topic,
		Token:         token,
		HTTPClient:    &http.Client{Timeout: 15 * time.Second},
		MaxErrorBytes: httpbody.MaxErrorBytes,
	}
}

// Notify publishes a single job match. reason is the AI scorer's
// human-readable explanation; if empty, a generic message is used
// instead.
func (n *Ntfy) Notify(ctx context.Context, job model.Job, reason string) error {
	title := sanitize.SingleLine(job.Title, maxTitleChars)
	company := sanitize.SingleLine(job.Company, maxTitleChars)
	location := sanitize.SingleLine(job.Location, maxTitleChars)

	body := sanitize.MultiLine(reason, maxReasonChars)
	if body == "" {
		body = fmt.Sprintf("New match: %s at %s", title, company)
	}
	if location != "" {
		body = fmt.Sprintf("%s\nLocation: %s", body, location)
	}
	body = sanitize.MultiLine(body, maxBodyChars)

	headers := map[string]string{
		"Title":    sanitize.SingleLine(fmt.Sprintf("%s: %s", company, title), maxTitleChars),
		"Priority": "default",
		"Tags":     "briefcase",
	}
	// Click becomes a tap target in the ntfy app. Only absolute http(s)
	// links are meaningful there; a javascript: or data: URL from a
	// hostile posting would otherwise be offered to the operator as one.
	if link := sanitize.Link(job.URL); link != "" {
		headers["Click"] = link
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
	// An error string is assembled from untrusted pieces (hostnames, status
	// text, response bodies) and is also written to the log, so collapse it
	// before either.
	return n.publish(ctx, sanitize.MultiLine(runErr.Error(), maxBodyChars), headers)
}

func (n *Ntfy) publish(ctx context.Context, body string, headers map[string]string) error {
	// The topic is interpolated into the path, so escape it rather than
	// letting a slash or query character in the configured value define a
	// different endpoint.
	endpoint := fmt.Sprintf("%s/%s", n.URL, url.PathEscape(n.Topic))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(body)))
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := httpbody.ReadAll(resp.Body, n.MaxErrorBytes)
		return fmt.Errorf("ntfy: unexpected status %d: %s", resp.StatusCode, string(errBody))
	}
	return nil
}

var _ Notifier = (*Ntfy)(nil)
