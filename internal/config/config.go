// Package config defines the go-get-a-job YAML configuration schema, along
// with loading and validation. Everything a user needs to customize
// (companies watched, filter keywords, AI profile text, notification
// target) lives here so the rest of the codebase never needs to change
// for a new company or a tweaked preference.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Config is the root go-get-a-job configuration document.
type Config struct {
	Sources []SourceConfig `yaml:"sources"`
	Filter  FilterConfig   `yaml:"filter"`
	AI      AIConfig       `yaml:"ai"`
	Notify  NotifyConfig   `yaml:"notify"`
	Store   StoreConfig    `yaml:"store"`
	Guard   GuardConfig    `yaml:"guard"`
}

// GuardConfig tunes the two things that protect the operator from a silent
// failure: the outbound request budget, and the alert that fires when a
// source stops returning postings.
//
// The failure both address is silence: a source that returns nothing -
// because its board was retired, its API changed, or its slug was mistyped -
// looks exactly like a quiet week, and the operator cannot tell the
// difference by reading a notification feed that is simply empty.
type GuardConfig struct {
	// DeadSourceRuns is how many consecutive successful runs a source may
	// return zero postings before a warning is sent, and how often that
	// warning repeats while the source stays silent. Defaults to 14.
	//
	// The default is deliberately long. This is a "the board is broken"
	// detector, not a "this company isn't hiring" detector, and those two
	// are indistinguishable from the response alone: a boutique
	// infrastructure company with three open roles legitimately has none for
	// weeks at a time, and a warning that fires whenever that happens is one
	// the operator learns to ignore - which is worse than no warning, since
	// it also buries the case that matters. Two weeks of a board answering
	// successfully with an empty list is long enough that "retired slug" or
	// "changed API" is the more likely explanation.
	//
	// To turn the guard off, set it high enough that it never fires. 0 (and
	// any negative value) is rejected rather than redefined, so that
	// "unset" and "off" can never be confused for each other.
	DeadSourceRuns int `yaml:"deadSourceRuns"`

	// MinRequestIntervalMs is the floor between outbound requests,
	// measured from the start of one to the start of the next. It is global
	// rather than per host. Defaults to 250.
	//
	// This is a politeness setting, and lowering it makes the tool worse,
	// not better: the hosts being polled are other people's infrastructure,
	// published for browsers rather than for a polling client, and a wide
	// per-posting fan-out is what a large board turns into. The reason it is
	// configurable at all is the opposite direction - raising it.
	// It is expressed in whole milliseconds because the config file is plain
	// YAML, where a bare "250ms" is ambiguous.
	MinRequestIntervalMs int `yaml:"minRequestIntervalMs"`

	// MaxRequestsPerRun caps the outbound requests one run may make.
	// Defaults to 10000. It catches a pagination loop rather than enforcing
	// a rate; reaching it means a bug, not a busy day.
	MaxRequestsPerRun int `yaml:"maxRequestsPerRun"`
}

// SourceConfig describes one company/board to watch. Which fields are
// required depends on Type; see Validate.
type SourceConfig struct {
	// Type selects the connector: "greenhouse", "lever", "ashby",
	// "smartrecruiters", "workday", "personio", "recruitee", "teamtailor",
	// or "workable".
	Type string `yaml:"type"`

	// DisplayName is shown in notifications and logs, e.g. "Grafana Labs".
	DisplayName string `yaml:"displayName"`

	// Company is the board token/company slug used by every connector other
	// than Workday (e.g. "grafanalabs" for Greenhouse; for Personio,
	// Recruitee, and Teamtailor it is the company subdomain, and for
	// Workable it is the account slug in the apply.workable.com URL).
	Company string `yaml:"company,omitempty"`

	// Tenant, Host, and Site are used only by the Workday connector, since
	// Workday has no single well-known API host. Host is the tenant's
	// full myworkdayjobs.com hostname (e.g.
	// "atlassian.wd3.myworkdayjobs.com"), found once via browser devtools
	// on the company's real careers page. Site is the career-site slug
	// (e.g. "Atlassian"), case-sensitive.
	Tenant string `yaml:"tenant,omitempty"`
	Host   string `yaml:"host,omitempty"`
	Site   string `yaml:"site,omitempty"`
}

// FilterConfig controls the cheap keyword pre-filter and the AI score
// threshold used to decide whether a posting is worth notifying about.
type FilterConfig struct {
	// Keywords are OR-matched, case-insensitively, against a job's title
	// and description. A job must match at least one to proceed to AI
	// scoring. Leave empty to disable the pre-filter (send everything to
	// the AI scorer, at higher cost).
	Keywords []string `yaml:"keywords"`

	// Locations decides which postings survive the cheap location
	// pre-filter. See LocationConfig.
	Locations LocationConfig `yaml:"locations"`

	// MinAIScore is the 0-1 threshold a job's AI relevance score must
	// reach to trigger a notification. Defaults to 0.7.
	MinAIScore float64 `yaml:"minAIScore"`
}

// Unmatched modes for LocationConfig.Unmatched.
const (
	// LocationUnmatchedReject drops a posting whose location names a place
	// that is not accepted. This is the default: the accept list is treated
	// as the full set of places worth considering.
	LocationUnmatchedReject = "reject"

	// LocationUnmatchedPass hands such a posting to the AI scorer instead.
	// It costs one AI call per posting that would otherwise have been
	// dropped, including foreign onsite roles.
	LocationUnmatchedPass = "pass"
)

// Bounds on operator-supplied location entries and AI instructions. The
// config lives in a ConfigMap, so these keep a mistaken or hostile edit from
// turning into unbounded work or unbounded log and prompt volume.
const (
	maxLocationEntries    = 200
	maxLocationEntryChars = 100
	maxInstructionsChars  = 4000
)

// LocationConfig decides which postings survive the cheap location
// pre-filter, by matching a posting's location string against a single list
// of places the operator can legally work from.
//
// It is a whitelist, not an allow/deny pair. Naming an accepted place is what
// sends a posting on to the AI scorer; nothing is accepted on a technicality,
// and there is no deny list to keep in sync - a place that is not listed
// simply does not match, so "Remote - Canada" cannot ride in on anything.
//
// Remote is deliberately not something the operator enumerates here. A
// location that signals remote, is empty, or is a filler value like "N/A"
// always reaches the AI scorer (see filter.MatchLocation for why): listing
// every phrasing a portal might use for "anywhere" is not a solvable
// problem, and the scorer already carries the relocation rule.
type LocationConfig struct {
	// Accept is an OR-matched list of places the operator can legally work
	// from, e.g. "Germany", "EMEA", "European Union". Matching is
	// word-boundary aware and case-insensitive, so "US" matches "Austin,
	// US" but not "Australia", and "Remote - Germany" matches "Germany".
	// Naming an accepted place sends the posting to the AI scorer. Empty
	// disables the location pre-filter: every location then proceeds to the
	// scorer.
	Accept []string `yaml:"accept"`

	// Unmatched decides what happens to a posting whose location names a
	// real place that is not accepted: "reject" (default) or "pass". It has
	// no effect while Accept is empty, since the pre-filter is then
	// disabled.
	//
	// A remote-signalling, empty, or uninformative location does not reach
	// this setting: it always proceeds to the scorer, because the
	// pre-filter cannot judge where such a role legally is and must not
	// drop one by guessing.
	Unmatched string `yaml:"unmatched"`
}

// UnmarshalYAML gives the removed forms of filter.locations a useful error
// instead of leaving yaml.v3 to either reject them unintelligibly or drop
// them silently. A silently ignored key is worse here than anywhere else in
// the config: losing "accept" or "deny" quietly changes which postings get
// scored, with nothing else in the run to signal that the key did nothing.
func (l *LocationConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.SequenceNode {
		return fmt.Errorf("filter.locations is now a mapping with an 'accept' list: as of v0.2.0 a plain list is no longer accepted, and the entries that used to be there belong under 'accept'")
	}

	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			switch node.Content[i].Value {
			case "allow":
				return fmt.Errorf("filter.locations.allow was renamed to filter.locations.accept in v0.3.0: rename the key, the entries themselves are unchanged")
			case "deny":
				return fmt.Errorf("filter.locations.deny was removed in v0.3.0: 'accept' is now a whitelist of the places you can work from, so a place you cannot work from no longer needs listing")
			}
		}
	}

	// A local alias type, so decoding does not recurse back into this
	// method.
	type rawLocationConfig LocationConfig
	var raw rawLocationConfig
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*l = LocationConfig(raw)
	return nil
}

// validate checks the accept list and the unmatched mode. It is called by
// Config.Validate.
func (l LocationConfig) validate() error {
	switch l.Unmatched {
	case "", LocationUnmatchedReject, LocationUnmatchedPass:
	default:
		return fmt.Errorf("filter.locations.unmatched: %q is not one of %q or %q", l.Unmatched, LocationUnmatchedReject, LocationUnmatchedPass)
	}

	if len(l.Accept) > maxLocationEntries {
		return fmt.Errorf("filter.locations.accept: %d entries is more than the %d allowed", len(l.Accept), maxLocationEntries)
	}
	for i, entry := range l.Accept {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			return fmt.Errorf("filter.locations.accept[%d]: entries must not be blank", i)
		}
		if utf8.RuneCountInString(trimmed) < 2 {
			return fmt.Errorf("filter.locations.accept[%d]: %q is too short to identify a location", i, trimmed)
		}
		if utf8.RuneCountInString(trimmed) > maxLocationEntryChars {
			return fmt.Errorf("filter.locations.accept[%d]: entries must be at most %d characters", i, maxLocationEntryChars)
		}
	}

	return nil
}

// AIConfig configures the relevance-scoring model call.
type AIConfig struct {
	// Provider is informational/for future multi-provider support.
	// Defaults to "deepseek".
	Provider string `yaml:"provider"`

	// APIKeyEnv is the name of the environment variable holding the API
	// key (populated from a Kubernetes Secret in deployment). The key
	// itself is never read from this config file.
	APIKeyEnv string `yaml:"apiKeyEnv"`

	// BaseURL is the OpenAI-compatible API base. Defaults to DeepSeek's
	// endpoint, but can point at any compatible provider.
	BaseURL string `yaml:"baseURL"`

	// Model is the model identifier to request. Defaults to
	// "deepseek-chat".
	Model string `yaml:"model"`

	// Profile is free-text describing what the user is looking for. It is
	// sent verbatim to the model alongside each candidate job, so it can
	// be edited at any time without any code change.
	Profile string `yaml:"profile"`

	// Instructions is operator-authored text appended to the scoring
	// prompt, for rules the profile is a poor place for - typically hard
	// constraints such as "a role that requires being legally based outside
	// Germany scores 0.2 or below".
	//
	// This is TRUSTED OPERATOR TEXT. It is appended to the system message,
	// where it sits alongside the output-format contract and the wording
	// that keeps job-posting text from being read as instructions. Do not
	// paste untrusted content here: unlike a job description, this text is
	// not fenced off as data, so untrusted text here would be obeyed.
	Instructions string `yaml:"instructions"`
}

// NotifyConfig selects and configures the notification channel.
type NotifyConfig struct {
	// Type selects the notifier. Currently only "ntfy" is implemented; the
	// interface is designed so more can be added later.
	Type string     `yaml:"type"`
	Ntfy NtfyConfig `yaml:"ntfy"`
}

// NtfyConfig configures the ntfy notifier.
type NtfyConfig struct {
	// URL is the base URL of the ntfy server (self-hosted or ntfy.sh).
	URL string `yaml:"url"`

	// Topic is the ntfy topic to publish matches to.
	Topic string `yaml:"topic"`

	// TokenEnv, if set, names the environment variable holding an ntfy
	// access token used as a Bearer auth header. Optional: omit if the
	// topic/server doesn't require auth.
	TokenEnv string `yaml:"tokenEnv,omitempty"`

	// MatchTiers maps a job's AI score onto the emoji and ntfy priority its
	// notification carries. Optional: omitting it uses DefaultMatchTiers.
	//
	// It is config rather than code because the right boundaries are a
	// property of the operator's own corpus, not of the program: the defaults
	// were chosen from a measured distribution of 46 notified matches, and a
	// different company list will produce a different shape.
	MatchTiers []MatchTier `yaml:"matchTiers,omitempty"`
}

// MatchTier is one band of the score-to-presentation mapping.
type MatchTier struct {
	// MinScore is the lowest score this tier covers. Tiers are ordered high
	// to low and the first one whose MinScore is <= the job's score wins.
	//
	// It is a pointer so that "not configured" stays distinguishable from
	// "configured as 0": the catch-all tier legitimately omits it, and a
	// plain float64 would silently turn every omitted tier into a catch-all
	// instead of reporting the mistake.
	MinScore *float64 `yaml:"minScore,omitempty"`

	// Emoji is what the notification title is prefixed with.
	Emoji string `yaml:"emoji"`

	// Priority is ntfy's own 1-5 scale (1 min, 5 urgent). Clients expose
	// one notification channel per priority, which is what lets the operator
	// mute the low tiers and keep the top one loud.
	Priority int `yaml:"priority"`
}

// DefaultMatchTiers is the tier mapping used when notify.ntfy.matchTiers is
// omitted entirely, so every existing config keeps working unchanged.
//
// The two boundaries are not round numbers picked for looks. Measured over 46
// real notified matches, scores cluster at 0.95, 0.85 and 0.72, with 14
// matches sitting exactly on 0.85 and 25 exactly on 0.72 - so 0.72 is a pile,
// not a boundary, and putting a tier edge there would flip half the corpus at
// once the first time the model shifted by a thousandth. 0.85 and 0.95 both
// fall in genuine gaps.
func DefaultMatchTiers() []MatchTier {
	great, perfect := 0.85, 0.95
	return []MatchTier{
		{MinScore: &perfect, Emoji: "💎", Priority: 5},
		{MinScore: &great, Emoji: "⭐", Priority: 4},
		{Emoji: "💼", Priority: 3},
	}
}

// Bounds on a matchTiers list. Ten is far more than any phone can usefully
// distinguish, and every extra tier is another chance for two of them to be
// unreachable in practice.
const maxMatchTiers = 10

// maxEmojiRunes bounds a configured emoji. Real emoji are 1-2 runes (a flag or
// a ZWJ sequence runs longer), so the cap only has to be generous enough to
// accept legitimate ones while still refusing a paragraph of text in a title.
const maxEmojiRunes = 8

// MatchTierFor returns the tier covering score: the first entry, in configured
// order, whose MinScore is <= score, or the catch-all when every MinScore is
// above it. Validation guarantees a catch-all exists, so this is total.
func (n NtfyConfig) MatchTierFor(score float64) MatchTier {
	for _, t := range n.MatchTiers {
		if t.MinScore == nil || score >= *t.MinScore {
			return t
		}
	}
	// Unreachable for a validated config; a zero tier is a safer answer than
	// a panic in a job watcher, whose whole purpose is to not go quiet.
	if len(n.MatchTiers) > 0 {
		return n.MatchTiers[len(n.MatchTiers)-1]
	}
	return MatchTier{Emoji: "💼", Priority: 3}
}

// UnreachableTiers reports the tiers that no posting can ever be placed in,
// because their MinScore sits below the threshold a posting must clear to be
// notified at all.
//
// This is not a validation error - the tiers are still well-formed, and the
// operator may be lowering minAIScore next week - but it is almost always a
// typo, and a tier that can never fire is otherwise completely invisible: it
// simply shows up as an emoji that never arrives.
func (n NtfyConfig) UnreachableTiers(minAIScore float64) []MatchTier {
	var out []MatchTier
	for _, t := range n.MatchTiers {
		if t.MinScore != nil && *t.MinScore < minAIScore {
			out = append(out, t)
		}
	}
	return out
}

func (n NtfyConfig) validateMatchTiers() error {
	if len(n.MatchTiers) > maxMatchTiers {
		return fmt.Errorf("notify.ntfy.matchTiers: %d entries is more than the %d allowed", len(n.MatchTiers), maxMatchTiers)
	}

	var prev *float64
	for i, t := range n.MatchTiers {
		if strings.TrimSpace(t.Emoji) == "" {
			return fmt.Errorf("notify.ntfy.matchTiers[%d]: emoji is required", i)
		}
		if numEmojiRunes := utf8.RuneCountInString(t.Emoji); numEmojiRunes > maxEmojiRunes {
			return fmt.Errorf("notify.ntfy.matchTiers[%d]: emoji is %d characters, more than the %d allowed", i, numEmojiRunes, maxEmojiRunes)
		}
		if t.Priority < 1 || t.Priority > 5 {
			return fmt.Errorf("notify.ntfy.matchTiers[%d]: priority must be 1-5 (ntfy's scale), got %d", i, t.Priority)
		}

		if t.MinScore != nil {
			if *t.MinScore < 0 || *t.MinScore > 1 {
				return fmt.Errorf("notify.ntfy.matchTiers[%d]: minScore must be between 0 and 1, got %v", i, *t.MinScore)
			}
			if prev != nil && *t.MinScore >= *prev {
				return fmt.Errorf("notify.ntfy.matchTiers[%d]: minScore %v must be lower than the previous tier's %v (tiers are matched top-down, so an ascending list would make the later entry dead)", i, *t.MinScore, *prev)
			}
			prev = t.MinScore
		}
	}

	if len(n.MatchTiers) > 0 && n.MatchTiers[len(n.MatchTiers)-1].MinScore != nil {
		return fmt.Errorf("notify.ntfy.matchTiers: the last entry must omit minScore so it acts as the catch-all; without one, a notified match could end up with no emoji")
	}

	return nil
}

// StoreConfig selects and configures the persistence backend.
type StoreConfig struct {
	// Type selects the store implementation. Currently only "sqlite" is
	// implemented.
	Type string `yaml:"type"`

	// Path is the filesystem path to the SQLite database file.
	Path string `yaml:"path"`
}

// Load reads, parses, defaults, and validates the config file at path.
func Load(path string) (*Config, error) {
	// The path comes from the operator-supplied --config flag, not from
	// untrusted input.
	data, err := os.ReadFile(path) //nolint:gosec // G304: operator-configured path
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", path, err)
	}

	return &cfg, nil
}

// Defaults for GuardConfig, mirrored in config.example.yaml.
const (
	DefaultDeadSourceRuns       = 14
	DefaultMinRequestIntervalMs = 250
	DefaultMaxRequestsPerRun    = 10_000
)

func (c *Config) applyDefaults() {
	if c.Filter.MinAIScore == 0 {
		c.Filter.MinAIScore = 0.7
	}
	if c.Filter.Locations.Unmatched == "" {
		c.Filter.Locations.Unmatched = LocationUnmatchedReject
	}
	if c.Guard.DeadSourceRuns == 0 {
		c.Guard.DeadSourceRuns = DefaultDeadSourceRuns
	}
	if c.Guard.MinRequestIntervalMs == 0 {
		c.Guard.MinRequestIntervalMs = DefaultMinRequestIntervalMs
	}
	if c.Guard.MaxRequestsPerRun == 0 {
		c.Guard.MaxRequestsPerRun = DefaultMaxRequestsPerRun
	}
	if c.AI.Provider == "" {
		c.AI.Provider = "deepseek"
	}
	if c.AI.BaseURL == "" {
		c.AI.BaseURL = "https://api.deepseek.com"
	}
	if c.AI.Model == "" {
		c.AI.Model = "deepseek-chat"
	}
	if c.Store.Type == "" {
		c.Store.Type = "sqlite"
	}
	if c.Notify.Type == "" {
		c.Notify.Type = "ntfy"
	}
	// Deliberately only when the whole list is absent. A partially
	// configured list is a mistake to report, not to silently pad: the
	// operator who wrote two tiers meant two tiers, and appending a default
	// catch-all would hide that they no longer control which emoji a
	// notification gets.
	if len(c.Notify.Ntfy.MatchTiers) == 0 && c.Notify.Type == "ntfy" {
		c.Notify.Ntfy.MatchTiers = DefaultMatchTiers()
	}
}

// Charset rules for values that are interpolated into a request path. A
// slash, question mark, or space here would let a config value - which is
// only semi-trusted, since it lives in a ConfigMap anyone with namespace
// write access can edit - redefine which endpoint the request goes to on
// an otherwise fixed origin. Rejecting them at startup turns a silent
// misdirection into a failed sync.
var (
	slugRE  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	hostRE  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)
	envRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	topicRE = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
)

// inClusterHosts are hostnames allowed to be reached over plain http. The
// ntfy Service is published as http://ntfy.<ns>.svc.cluster.local and the
// hop never leaves the pod network, so requiring https there would be
// wrong; everything else must be encrypted, because the bearer token sent
// with each publish would otherwise be readable in transit.
var inClusterHosts = []string{".svc.cluster.local", ".svc", ".cluster.local"}

func isInClusterHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	lower := strings.ToLower(host)
	for _, suffix := range inClusterHosts {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// validateServiceURL checks that an operator-supplied base URL is an
// absolute, unambiguous HTTP(S) endpoint. allowHTTP relaxes the scheme
// requirement for in-cluster hosts only.
//
// userinfo is rejected outright: Go sends it as a basic-auth header, which
// means a URL typed into a ConfigMap could carry credentials - or, worse,
// silently override the real bearer token on the request.
func validateServiceURL(field, raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %q is not a valid URL: %w", field, raw, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowHTTP || !isInClusterHost(u.Hostname()) {
			return fmt.Errorf("%s: %q must use https (plain http is only allowed for in-cluster or loopback hosts)", field, raw)
		}
	default:
		return fmt.Errorf("%s: %q must use https, got scheme %q", field, raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: %q has no host", field, raw)
	}
	if u.User != nil {
		return fmt.Errorf("%s: %q must not contain a username or password", field, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s: %q must not contain a query string or fragment", field, raw)
	}
	return nil
}

var validSourceTypes = map[string]bool{
	"greenhouse":      true,
	"lever":           true,
	"ashby":           true,
	"smartrecruiters": true,
	"workday":         true,
	"personio":        true,
	"recruitee":       true,
	"teamtailor":      true,
	"workable":        true,
}

// Validate checks the config for missing/inconsistent required fields. It
// is called automatically by Load, but is exported so callers building a
// Config programmatically (e.g. in tests) can validate it too.
func (c *Config) Validate() error {
	if len(c.Sources) == 0 {
		return fmt.Errorf("at least one entry under 'sources' is required")
	}

	for i, s := range c.Sources {
		name := s.DisplayName
		if name == "" {
			name = fmt.Sprintf("#%d", i)
		}

		if !validSourceTypes[s.Type] {
			return fmt.Errorf("sources[%d] (%s): unknown type %q", i, name, s.Type)
		}
		if s.DisplayName == "" {
			return fmt.Errorf("sources[%d]: displayName is required", i)
		}

		if s.Type == "workday" {
			if s.Tenant == "" || s.Host == "" || s.Site == "" {
				return fmt.Errorf("sources[%d] (%s): workday sources require tenant, host, and site", i, name)
			}
			if !slugRE.MatchString(s.Tenant) {
				return fmt.Errorf("sources[%d] (%s): tenant %q must match %s", i, name, s.Tenant, slugRE)
			}
			if !slugRE.MatchString(s.Site) {
				return fmt.Errorf("sources[%d] (%s): site %q must match %s", i, name, s.Site, slugRE)
			}
			if !hostRE.MatchString(s.Host) {
				return fmt.Errorf("sources[%d] (%s): host %q must be a bare hostname (no scheme, port, path, or whitespace), matching %s", i, name, s.Host, hostRE)
			}
		} else {
			if s.Company == "" {
				return fmt.Errorf("sources[%d] (%s): company is required for %s sources", i, name, s.Type)
			}
			if !slugRE.MatchString(s.Company) {
				return fmt.Errorf("sources[%d] (%s): company %q must match %s", i, name, s.Company, slugRE)
			}
		}
	}

	if c.Filter.MinAIScore < 0 || c.Filter.MinAIScore > 1 {
		return fmt.Errorf("filter.minAIScore must be between 0 and 1, got %v", c.Filter.MinAIScore)
	}

	if err := c.Filter.Locations.validate(); err != nil {
		return err
	}

	if n := utf8.RuneCountInString(c.AI.Instructions); n > maxInstructionsChars {
		return fmt.Errorf("ai.instructions: %d characters is more than the %d allowed", n, maxInstructionsChars)
	}

	if c.AI.APIKeyEnv == "" {
		return fmt.Errorf("ai.apiKeyEnv is required (name of the environment variable holding the API key)")
	}
	if !envRE.MatchString(c.AI.APIKeyEnv) {
		return fmt.Errorf("ai.apiKeyEnv: %q is not a valid environment variable name", c.AI.APIKeyEnv)
	}
	if err := validateServiceURL("ai.baseURL", c.AI.BaseURL, false); err != nil {
		return err
	}

	switch c.Notify.Type {
	case "ntfy":
		if c.Notify.Ntfy.URL == "" || c.Notify.Ntfy.Topic == "" {
			return fmt.Errorf("notify.ntfy.url and notify.ntfy.topic are required when notify.type is ntfy")
		}
		if err := validateServiceURL("notify.ntfy.url", c.Notify.Ntfy.URL, true); err != nil {
			return err
		}
		if !topicRE.MatchString(c.Notify.Ntfy.Topic) {
			return fmt.Errorf("notify.ntfy.topic: %q must match %s (ntfy topics are 1-64 characters of A-Z, a-z, 0-9, -, _)", c.Notify.Ntfy.Topic, topicRE)
		}
		if c.Notify.Ntfy.TokenEnv != "" && !envRE.MatchString(c.Notify.Ntfy.TokenEnv) {
			return fmt.Errorf("notify.ntfy.tokenEnv: %q is not a valid environment variable name", c.Notify.Ntfy.TokenEnv)
		}
		if err := c.Notify.Ntfy.validateMatchTiers(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("notify.type: unknown or unsupported type %q", c.Notify.Type)
	}

	switch c.Store.Type {
	case "sqlite":
		if c.Store.Path == "" {
			return fmt.Errorf("store.path is required when store.type is sqlite")
		}
	default:
		return fmt.Errorf("store.type: unknown or unsupported type %q", c.Store.Type)
	}

	// Every guard value is a limit on how hard this program may hit other
	// people's servers or on how noisily it may alert. Zero means "unset"
	// (applyDefaults has already replaced it by the time a config is in
	// use); a negative value can only be a typo, and silently treating it as
	// a very large budget is exactly the kind of quiet misreading this whole
	// guard exists to prevent.
	if c.Guard.DeadSourceRuns < 0 {
		return fmt.Errorf("guard.deadSourceRuns must not be negative, got %d", c.Guard.DeadSourceRuns)
	}
	if c.Guard.MinRequestIntervalMs < 0 {
		return fmt.Errorf("guard.minRequestIntervalMs must not be negative, got %d", c.Guard.MinRequestIntervalMs)
	}
	if c.Guard.MaxRequestsPerRun < 0 {
		return fmt.Errorf("guard.maxRequestsPerRun must not be negative, got %d", c.Guard.MaxRequestsPerRun)
	}

	return nil
}
