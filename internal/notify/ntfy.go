package notify

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
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

	// maxLocationSuffixChars bounds the hint appended to the title. It is
	// small on purpose: the suffix exists to tell two otherwise identical
	// notifications apart, so it only has to be long enough to distinguish
	// them, and every rune it takes is a rune unavailable to the job title.
	maxLocationSuffixChars = 28

	// maxTagChars bounds the company tag, and maxMetaFieldChars the
	// individual metadata labels.
	maxTagChars       = 30
	maxMetaFieldChars = 56
	maxSignalsChars   = 120

	// fallbackTag is used when a display name slugifies to nothing at all.
	fallbackTag = "job"

	// locationSeparator rejoins the parts of a title. " · " reads as a
	// separator in every client in a way a bare hyphen does not.
	locationSeparator = " · "

	// titleSeparator sits between the tier emoji and the company name.
	titleSeparator = " "

	// maxEmojiRunes bounds the tier emoji. It mirrors the bound config
	// validation enforces, and exists here so the title budget can be
	// reserved against a known worst case rather than whatever a caller
	// happens to supply.
	maxEmojiRunes = 8

	// truncationMarkerChars is how many runes sanitize.TruncateRunes appends
	// when it cuts. It is reserved as part of the title budget so the
	// marker cannot push the location suffix back out of the title it was
	// just protected from.
	truncationMarkerChars = 3
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

	// MatchTiers maps a score onto the emoji and priority a notification is
	// delivered with. Empty means the built-in defaults; see
	// config.DefaultMatchTiers.
	MatchTiers []config.MatchTier

	HTTPClient *http.Client
	// MaxErrorBytes caps how much of an error response body is included in
	// the returned error; see internal/httpbody.
	MaxErrorBytes int64

	// Now defaults to time.Now if nil; overridable so the "Posted N days
	// ago" line can be asserted without depending on the wall clock.
	Now func() time.Time
}

// NewNtfy builds an Ntfy notifier for the given server URL, topic, and
// optional auth token (pass "" if the topic doesn't require auth). An empty
// tiers list selects the built-in defaults.
func NewNtfy(url, topic, token string, tiers []config.MatchTier) *Ntfy {
	if len(tiers) == 0 {
		tiers = config.DefaultMatchTiers()
	}
	return &Ntfy{
		URL:           strings.TrimRight(url, "/"),
		Topic:         topic,
		Token:         token,
		MatchTiers:    tiers,
		HTTPClient:    &http.Client{Timeout: 15 * time.Second},
		MaxErrorBytes: httpbody.MaxErrorBytes,
	}
}

// Notify publishes a single job match.
//
// The message is laid out so the whole of it is readable from the notification
// shade without opening the app:
//
//	<emoji> <Company>: <Title> · <location>
//	<department> · <workplace> · <employment> · Posted <age>
//	Matched: <signals>
//
//	<scorer's reason>
//
// The emoji is the first character of the title and encodes the score. The
// location is in the title because the title is what a collapsed notification
// shows, and without it two postings for the same role at the same company are
// indistinguishable.
func (n *Ntfy) Notify(ctx context.Context, match Match) error {
	job := match.Job
	tier := n.tierFor(match.Score)

	title := n.title(match, tier.Emoji)

	headers := map[string]string{
		"Title":    title,
		"Priority": strconv.Itoa(tier.Priority),
		// A company slug, not an icon: ntfy converts a tag matching an emoji
		// short code into an emoji prepended to the title, so a "briefcase"
		// tag would render the 💼 next to the tier emoji and put two emoji on
		// every notification. A slug matches no short code and is instead
		// listed beneath the message, where it doubles as the thing that
		// makes the feed skimmable.
		"Tags": companyTag(job.Company),
	}
	// Click becomes a tap target in the ntfy app. Only absolute http(s)
	// links are meaningful there; a javascript: or data: URL from a
	// hostile posting would otherwise be offered to the operator as one.
	if link := sanitize.Link(job.URL); link != "" {
		headers["Click"] = link
	}

	return n.publish(ctx, n.buildBody(job, match), headers)
}

// title renders "<emoji> <Company>: <Title> · <Location>", reserving room for
// the emoji and the location suffix before truncating rather than after.
//
// Truncating the assembled string instead would let a long posting title eat
// the suffix from the end - deleting exactly the field the suffix exists to
// provide, and doing it silently and only for long titles, which is the worst
// way for it to fail. The emoji has to be reserved for the same reason, and
// less obviously: a two-rune prefix is enough to push the suffix off the end
// of a title already sitting at the limit.
func (n *Ntfy) title(match Match, emoji string) string {
	job := match.Job
	prefix := sanitize.SingleLine(emoji, maxEmojiRunes)

	company := sanitize.SingleLine(job.Company, maxTitleChars)
	title := sanitize.SingleLine(job.Title, maxTitleChars)

	suffix := locationHint(job.Location, match.LocationRule)

	// Everything that is not the company/title portion, including the
	// truncation marker the name may have appended.
	reserved := utf8.RuneCountInString(prefix) + utf8.RuneCountInString(titleSeparator) + truncationMarkerChars
	if suffix != "" {
		reserved += utf8.RuneCountInString(locationSeparator) + utf8.RuneCountInString(suffix)
	}

	budget := maxTitleChars - reserved
	if budget < 1 {
		// A pathological location or emoji; drop the suffix rather than the
		// company and job title, which is what the notification is for.
		suffix = ""
		budget = maxTitleChars - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(titleSeparator) - truncationMarkerChars
	}

	name := sanitize.SingleLine(fmt.Sprintf("%s: %s", company, title), budget)
	if prefix != "" {
		name = prefix + titleSeparator + name
	}
	if suffix == "" {
		return name
	}
	return name + locationSeparator + suffix
}

// buildBody assembles the notification body: metadata first, prose last.
//
// The order matters for the collapsed notification, which shows only the first
// line or two. Metadata goes first because it is short, structured, and
// complete on its own; the scorer's reason goes last because it is a few
// hundred characters of prose whose meaning usually lands at the end of the
// second sentence. Reversed, the metadata would be pushed out of the visible
// area by text that does not need to be there.
func (n *Ntfy) buildBody(job model.Job, match Match) string {
	var lines []string

	if meta := metadataLine(job, n.now()); meta != "" {
		lines = append(lines, meta)
	}
	if signals := sanitize.SingleLine(strings.Join(match.Signals, ", "), maxSignalsChars); signals != "" {
		lines = append(lines, "Matched: "+signals)
	}

	reason := sanitize.MultiLine(match.Reason, maxReasonChars)
	if reason == "" {
		reason = fmt.Sprintf("New match: %s at %s", sanitize.SingleLine(job.Title, maxTitleChars), sanitize.SingleLine(job.Company, maxTitleChars))
	}
	if len(lines) == 0 {
		return sanitize.MultiLine(reason, maxBodyChars)
	}
	// A blank line separates the structured head from the prose, so the
	// reason does not read as one more field.
	return sanitize.MultiLine(strings.Join(lines, "\n")+"\n\n"+reason, maxBodyChars)
}

// metadataLine renders the posting's own labels as one line, omitting each
// field its source did not provide.
//
// Omission rather than a placeholder is deliberate: these fields come from
// different ATS APIs that publish different subsets, so a row of "N/A · N/A"
// on a Greenhouse posting would be noise presenting itself as information.
func metadataLine(job model.Job, now time.Time) string {
	parts := make([]string, 0, 4)
	for _, v := range []string{job.Department, job.WorkplaceType, job.EmploymentType} {
		if s := sanitize.SingleLine(v, maxMetaFieldChars); s != "" {
			parts = append(parts, s)
		}
	}
	if age := postingAge(job.PostedAt, now); age != "" {
		parts = append(parts, "Posted "+age)
	}
	return strings.Join(parts, " · ")
}

// postingAge renders how long ago a posting went up, at a granularity that
// answers "is this still worth applying to" rather than "exactly when".
// Returns "" when the source published no timestamp.
func postingAge(postedAt, now time.Time) string {
	if postedAt.IsZero() {
		return ""
	}
	// Workday exposes only a relative string, and clocks skew, so a future
	// timestamp is a normal artefact rather than an error. "In 3 hours"
	// would be nonsense on a job posting; "today" is closer to the truth.
	if !postedAt.Before(now) {
		return "today"
	}
	switch d := now.Sub(postedAt); {
	case d < 24*time.Hour:
		if hours := int(d.Hours()); hours >= 1 {
			if hours == 1 {
				return "1 hour ago"
			}
			return fmt.Sprintf("%d hours ago", hours)
		}
		return "today"
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// locationHint reduces a location to the shortest fragment that still
// distinguishes it from a neighbouring one.
//
// Several boards list a posting against every place it may be filled in
// ("Remote, Germany; Remote, United Kingdom"), and the full value overruns the
// title. Which segment to show cannot be decided from the location string
// alone, because a board's own order is no guide to relevance: Canonical leads
// "Home Based - Americas; Home based - EMEA" with the region that rules the
// posting out for a reader in Germany. matchedRule is the configured entry that
// selected this posting, which turns the choice into a fact rather than a
// guess. When it is empty or absent from the string, the first segment is used
// as before - there is nothing better to go on.
func locationHint(location, matchedRule string) string {
	loc := sanitize.SingleLine(location, maxTitleChars)
	if loc == "" {
		return ""
	}
	if workable := workableSegment(loc, matchedRule); workable != "" {
		loc = workable
	} else if first, _, found := strings.Cut(loc, ";"); found {
		loc = strings.TrimSpace(first)
	}
	if utf8.RuneCountInString(loc) <= maxLocationSuffixChars {
		return loc
	}
	return strings.TrimSpace(sanitize.TruncateRunes(loc, maxLocationSuffixChars-1)) + "…"
}

// workableSegment returns the ";"-separated part of loc that names the entry
// which selected the posting, or "" when that cannot be determined.
//
// The comparison is a plain case-insensitive substring, deliberately looser
// than the filter's own word-boundary match: the filter has already decided
// that this entry applies to the whole string, so the only question left is
// which part of it carries the entry. A miss costs nothing, because "" falls
// back to the previous behaviour of showing the first segment.
func workableSegment(loc, matchedRule string) string {
	rule := strings.TrimSpace(matchedRule)
	if rule == "" || !strings.Contains(loc, ";") {
		return ""
	}
	needle := strings.ToLower(rule)
	for _, segment := range strings.Split(loc, ";") {
		trimmed := strings.TrimSpace(segment)
		if strings.Contains(strings.ToLower(trimmed), needle) {
			return trimmed
		}
	}
	return ""
}

// companyTag builds the ntfy tag shown under a notification from a display
// name.
//
// Slugifying is required rather than cosmetic: a display name containing a
// comma would split the Tags header into two tags, so "Solo.io, Inc" would
// silently arrive as two labels instead of one.
func companyTag(company string) string {
	var b strings.Builder
	lastUnderscore := true // a leading separator must not produce a leading _
	for _, r := range strings.ToLower(company) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteRune('_')
			lastUnderscore = true
		}
	}
	tag := strings.Trim(b.String(), "_")
	if tag == "" {
		return fallbackTag
	}
	return sanitize.TruncateRunes(tag, maxTagChars)
}

// tierFor maps a score onto its tier, falling back to the last configured tier
// so a malformed list cannot produce an emoji-less notification.
//
// The mapping itself lives in config.MatchTierFor because the validation rules
// ("ordered high to low", "a catch-all must exist") and the lookup rule are the
// same decision seen twice; two copies would drift.
func (n *Ntfy) tierFor(score float64) config.MatchTier {
	return config.NtfyConfig{MatchTiers: n.MatchTiers}.MatchTierFor(score)
}

func (n *Ntfy) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
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

// NotifyWarning publishes a low-noise alert about a successful run that
// nonetheless needs attention, such as a source that has stopped returning
// postings.
//
// The title is prefixed so the message is differentiable from a job match at a
// glance in the notification list - the operator's phone shows the title first,
// and a "warning"-titled message must never be mistaken for a posting worth
// applying to. The tag and priority reinforce that in clients that render them.
func (n *Ntfy) NotifyWarning(ctx context.Context, title, body string) error {
	headers := map[string]string{
		"Title":    sanitize.SingleLine("go-get-a-job warning: "+title, maxTitleChars),
		"Priority": "default",
		"Tags":     "warning",
	}
	return n.publish(ctx, sanitize.MultiLine(body, maxBodyChars), headers)
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
