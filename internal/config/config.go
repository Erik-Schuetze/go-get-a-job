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

	"gopkg.in/yaml.v3"
)

// Config is the root go-get-a-job configuration document.
type Config struct {
	Sources []SourceConfig `yaml:"sources"`
	Filter  FilterConfig   `yaml:"filter"`
	AI      AIConfig       `yaml:"ai"`
	Notify  NotifyConfig   `yaml:"notify"`
	Store   StoreConfig    `yaml:"store"`
}

// SourceConfig describes one company/board to watch. Which fields are
// required depends on Type; see Validate.
type SourceConfig struct {
	// Type selects the connector: "greenhouse", "lever", "ashby",
	// "smartrecruiters", or "workday".
	Type string `yaml:"type"`

	// DisplayName is shown in notifications and logs, e.g. "Grafana Labs".
	DisplayName string `yaml:"displayName"`

	// Company is the board token/company slug used by Greenhouse, Lever,
	// Ashby, and SmartRecruiters (e.g. "grafanalabs").
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

	// Locations, if non-empty, is an OR-matched allow-list of substrings
	// checked against a job's location string. Empty means no location
	// filtering.
	Locations []string `yaml:"locations"`

	// MinAIScore is the 0-1 threshold a job's AI relevance score must
	// reach to trigger a notification. Defaults to 0.7.
	MinAIScore float64 `yaml:"minAIScore"`
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

func (c *Config) applyDefaults() {
	if c.Filter.MinAIScore == 0 {
		c.Filter.MinAIScore = 0.7
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

	return nil
}
