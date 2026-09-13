package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}

const validConfig = `
sources:
  - type: greenhouse
    company: grafanalabs
    displayName: "Grafana Labs"
  - type: workday
    tenant: atlassian
    host: atlassian.wd3.myworkdayjobs.com
    site: Atlassian
    displayName: "Atlassian"

filter:
  keywords: ["platform engineer", "crossplane"]
  locations:
    accept: ["Germany", "EMEA", "European Union"]
    unmatched: reject
  minAIScore: 0.8

ai:
  apiKeyEnv: DEEPSEEK_API_KEY
  profile: "Looking for platform engineering roles."
  instructions: "A role that requires relocation outside Germany scores 0.2 or below."

notify:
  type: ntfy
  ntfy:
    url: http://ntfy.go-get-a-job.svc.cluster.local
    topic: job-matches

store:
  type: sqlite
  path: /data/go-get-a-job.db
`

func TestLoad_Valid(t *testing.T) {
	path := writeTempConfig(t, validConfig)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if len(cfg.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(cfg.Sources))
	}
	if cfg.Filter.MinAIScore != 0.8 {
		t.Errorf("expected minAIScore 0.8, got %v", cfg.Filter.MinAIScore)
	}
	// Defaults should be applied.
	if cfg.AI.Provider != "deepseek" {
		t.Errorf("expected default AI provider 'deepseek', got %q", cfg.AI.Provider)
	}
	if cfg.AI.BaseURL != "https://api.deepseek.com" {
		t.Errorf("expected default AI base URL, got %q", cfg.AI.BaseURL)
	}
	if cfg.AI.Model != "deepseek-chat" {
		t.Errorf("expected default AI model, got %q", cfg.AI.Model)
	}
}

func TestLoad_DefaultsAppliedWhenMinAIScoreZero(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{{Type: "greenhouse", Company: "foo", DisplayName: "Foo"}},
		AI:      AIConfig{APIKeyEnv: "KEY"},
		Notify:  NotifyConfig{Type: "ntfy", Ntfy: NtfyConfig{URL: "http://x", Topic: "t"}},
		Store:   StoreConfig{Type: "sqlite", Path: "/data/x.db"},
	}
	cfg.applyDefaults()
	if cfg.Filter.MinAIScore != 0.7 {
		t.Errorf("expected default minAIScore 0.7, got %v", cfg.Filter.MinAIScore)
	}
	if cfg.Filter.Locations.Unmatched != LocationUnmatchedReject {
		t.Errorf("expected an omitted locations.unmatched to default to %q, got %q",
			LocationUnmatchedReject, cfg.Filter.Locations.Unmatched)
	}
}

func TestLoad_LocationsParsed(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	locs := cfg.Filter.Locations
	if len(locs.Accept) != 3 || locs.Accept[0] != "Germany" {
		t.Errorf("expected the accept list to be parsed in order, got %#v", locs.Accept)
	}
	if locs.Unmatched != LocationUnmatchedReject {
		t.Errorf("expected unmatched %q, got %q", LocationUnmatchedReject, locs.Unmatched)
	}
	if cfg.AI.Instructions == "" {
		t.Error("expected ai.instructions to be parsed")
	}
}

// TestLoad_LocationsOldListFormFailsWithGuidance covers the upgrade path for
// a config written against v0.1.0, where filter.locations was a plain list.
// yaml.v3's own message ("cannot unmarshal !!seq into
// config.LocationConfig") names the Go type and not the fix, so
// LocationConfig.UnmarshalYAML replaces it.
func TestLoad_LocationsOldListFormFailsWithGuidance(t *testing.T) {
	old := strings.Replace(validConfig,
		"  locations:\n    accept: [\"Germany\", \"EMEA\", \"European Union\"]\n    unmatched: reject\n",
		"  locations: [\"Germany\", \"Remote\"]\n", 1)
	if old == validConfig {
		t.Fatal("test setup failed: the locations block was not found in validConfig")
	}

	_, err := Load(writeTempConfig(t, old))
	if err == nil {
		t.Fatal("expected the old list form to fail, got nil")
	}
	for _, want := range []string{"accept", "v0.2.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the migration error to mention %q, got: %v", want, err)
		}
	}
}

// TestLoad_LocationsRemovedKeysFailWithGuidance covers the v0.2.0 -> v0.3.0
// upgrade path. Both keys used to change which postings were scored, so
// dropping either one silently would leave a run that looks healthy while
// filtering on something the operator did not write.
func TestLoad_LocationsRemovedKeysFailWithGuidance(t *testing.T) {
	tests := []struct {
		name    string
		block   string
		wantErr []string
	}{
		{
			name:    "allow is renamed",
			block:   "  locations:\n    allow: [\"Germany\"]\n",
			wantErr: []string{"filter.locations.allow", "filter.locations.accept"},
		},
		{
			name:    "deny is removed",
			block:   "  locations:\n    accept: [\"Germany\"]\n    deny: [\"Canada\"]\n",
			wantErr: []string{"filter.locations.deny", "whitelist"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replaced := strings.Replace(validConfig,
				"  locations:\n    accept: [\"Germany\", \"EMEA\", \"European Union\"]\n    unmatched: reject\n",
				tt.block, 1)
			if replaced == validConfig {
				t.Fatal("test setup failed: the locations block was not found in validConfig")
			}

			_, err := Load(writeTempConfig(t, replaced))
			if err == nil {
				t.Fatal("expected a removed key to fail, got nil")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected the migration error to mention %q, got: %v", want, err)
				}
			}
		})
	}
}

func TestValidate_LocationsUnmatchedMode(t *testing.T) {
	tests := []struct {
		name      string
		unmatched string
		wantErr   bool
	}{
		{"empty is allowed and defaulted", "", false},
		{"reject", LocationUnmatchedReject, false},
		{"pass", LocationUnmatchedPass, false},
		{"unknown", "maybe", true},
		{"wrong case", "Reject", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Filter.Locations.Unmatched = tt.unmatched
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected an error for unmatched %q, got nil", tt.unmatched)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for unmatched %q: %v", tt.unmatched, err)
			}
		})
	}
}

func TestValidate_LocationEntries(t *testing.T) {
	tests := []struct {
		name    string
		accept  []string
		wantErr string
	}{
		{"ordinary entries", []string{"Germany", "European Union"}, ""},
		{"two characters is the minimum", []string{"US"}, ""},
		{"empty list disables the filter", nil, ""},
		{"blank entry", []string{""}, "must not be blank"},
		{"whitespace-only entry", []string{"   "}, "must not be blank"},
		{"single character entry", []string{"D"}, "too short"},
		{"oversized entry", []string{strings.Repeat("x", maxLocationEntryChars+1)}, "at most"},
		{"too many entries", makeEntries(maxLocationEntries + 1), "more than"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Filter.Locations.Accept = tt.accept

			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected an error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidate_InstructionsLength(t *testing.T) {
	cfg := baseValidConfig()
	cfg.AI.Instructions = strings.Repeat("x", maxInstructionsChars)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected an instructions value exactly at the cap to be accepted: %v", err)
	}

	cfg.AI.Instructions = strings.Repeat("x", maxInstructionsChars+1)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an oversized ai.instructions to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "ai.instructions") {
		t.Errorf("expected the error to name ai.instructions, got: %v", err)
	}
}

func makeEntries(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "place-" + strconv.Itoa(i)
	}
	return out
}

func TestValidate_NoSources(t *testing.T) {
	cfg := &Config{}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing sources, got nil")
	}
}

func TestValidate_UnknownSourceType(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{{Type: "bogus", DisplayName: "X", Company: "x"}},
		AI:      AIConfig{APIKeyEnv: "KEY"},
		Notify:  NotifyConfig{Type: "ntfy", Ntfy: NtfyConfig{URL: "http://x", Topic: "t"}},
		Store:   StoreConfig{Type: "sqlite", Path: "/data/x.db"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for unknown source type, got nil")
	}
}

func TestValidate_WorkdayMissingFields(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{{Type: "workday", DisplayName: "Atlassian", Tenant: "atlassian"}},
		AI:      AIConfig{APIKeyEnv: "KEY"},
		Notify:  NotifyConfig{Type: "ntfy", Ntfy: NtfyConfig{URL: "http://x", Topic: "t"}},
		Store:   StoreConfig{Type: "sqlite", Path: "/data/x.db"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for workday source missing host/site, got nil")
	}
}

func TestValidate_NonWorkdayMissingCompany(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{{Type: "greenhouse", DisplayName: "X"}},
		AI:      AIConfig{APIKeyEnv: "KEY"},
		Notify:  NotifyConfig{Type: "ntfy", Ntfy: NtfyConfig{URL: "http://x", Topic: "t"}},
		Store:   StoreConfig{Type: "sqlite", Path: "/data/x.db"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing company, got nil")
	}
}

func TestValidate_BadMinAIScore(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{{Type: "greenhouse", DisplayName: "X", Company: "x"}},
		Filter:  FilterConfig{MinAIScore: 1.5},
		AI:      AIConfig{APIKeyEnv: "KEY"},
		Notify:  NotifyConfig{Type: "ntfy", Ntfy: NtfyConfig{URL: "http://x", Topic: "t"}},
		Store:   StoreConfig{Type: "sqlite", Path: "/data/x.db"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for out-of-range minAIScore, got nil")
	}
}

func TestValidate_MissingAPIKeyEnv(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{{Type: "greenhouse", DisplayName: "X", Company: "x"}},
		Notify:  NotifyConfig{Type: "ntfy", Ntfy: NtfyConfig{URL: "http://x", Topic: "t"}},
		Store:   StoreConfig{Type: "sqlite", Path: "/data/x.db"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing ai.apiKeyEnv, got nil")
	}
}

func TestValidate_NtfyMissingURLOrTopic(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{{Type: "greenhouse", DisplayName: "X", Company: "x"}},
		AI:      AIConfig{APIKeyEnv: "KEY"},
		Notify:  NotifyConfig{Type: "ntfy"},
		Store:   StoreConfig{Type: "sqlite", Path: "/data/x.db"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing ntfy url/topic, got nil")
	}
}

func TestValidate_SqliteMissingPath(t *testing.T) {
	cfg := &Config{
		Sources: []SourceConfig{{Type: "greenhouse", DisplayName: "X", Company: "x"}},
		AI:      AIConfig{APIKeyEnv: "KEY"},
		Notify:  NotifyConfig{Type: "ntfy", Ntfy: NtfyConfig{URL: "http://x", Topic: "t"}},
		Store:   StoreConfig{Type: "sqlite"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing sqlite path, got nil")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	if _, err := Load("/nonexistent/path/config.yaml"); err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeTempConfig(t, "sources: [this is not valid: yaml: at all:")
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

// TestLoad_ExampleConfig guards against the shipped example config drifting
// out of sync with the schema/validation rules.
func TestLoad_ExampleConfig(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config", "config.example.yaml"))
	if err != nil {
		t.Fatalf("config/config.example.yaml must always load cleanly: %v", err)
	}
	if len(cfg.Sources) == 0 {
		t.Fatal("expected example config to define at least one source")
	}
}

// baseValidConfig returns a config that passes Validate, for tests that need
// to change exactly one field and assert on the error it produces.
func baseValidConfig() *Config {
	return &Config{
		Sources: []SourceConfig{{
			Type:        "greenhouse",
			DisplayName: "Grafana Labs",
			Company:     "grafanalabs",
		}},
		Filter: FilterConfig{MinAIScore: 0.7},
		AI: AIConfig{
			APIKeyEnv: "DEEPSEEK_API_KEY",
			BaseURL:   "https://api.deepseek.com",
			Model:     "deepseek-chat",
			Profile:   "platform engineering",
		},
		Notify: NotifyConfig{
			Type: "ntfy",
			Ntfy: NtfyConfig{URL: "http://ntfy.go-get-a-job.svc.cluster.local", Topic: "job-matches"},
		},
		Store: StoreConfig{Type: "sqlite", Path: "/data/go-get-a-job.db"},
	}
}

func TestValidate_AIBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"an https endpoint is accepted", "https://api.deepseek.com", false},
		{"an https endpoint with a path is accepted", "https://api.deepseek.com/v1", false},
		{"plain http is rejected - the API key travels in this request", "http://api.deepseek.com", true},
		{"a scheme-less value is rejected", "api.deepseek.com", true},
		{"a relative value is rejected", "/v1/chat", true},
		{"a non-http scheme is rejected", "file:///etc/passwd", true},
		{"credentials are rejected - they would override the bearer token", "https://user:pass@api.deepseek.com", true},
		{"a query string is rejected", "https://api.deepseek.com?x=1", true},
		{"a fragment is rejected", "https://api.deepseek.com#x", true},
		{"an empty host is rejected", "https://", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.AI.BaseURL = tt.url

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected ai.baseURL %q to be rejected", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected ai.baseURL %q to be accepted, got %v", tt.url, err)
			}
		})
	}
}

func TestValidate_NtfyURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https is accepted", "https://ntfy.example.com", false},
		{"the in-cluster Service name over http is accepted", "http://ntfy.go-get-a-job.svc.cluster.local", false},
		{"a bare .svc name over http is accepted", "http://ntfy.go-get-a-job.svc", false},
		{"a .cluster.local name over http is accepted", "http://ntfy.default.cluster.local", false},
		{"localhost over http is accepted", "http://localhost:8080", false},
		{"a loopback address over http is accepted", "http://127.0.0.1:8080", false},
		{"IPv6 loopback over http is accepted", "http://[::1]:8080", false},
		{"plain http to a public host is rejected - the token would be readable", "http://ntfy.example.com", true},
		{"plain http to a LAN address is rejected", "http://192.168.10.50", true},
		{"a scheme-less value is rejected", "ntfy.example.com", true},
		{"a non-http scheme is rejected", "ftp://ntfy.example.com", true},
		{"credentials are rejected", "https://user:pass@ntfy.example.com", true},
		{"a query string is rejected", "https://ntfy.example.com?x=1", true},
		{"a fragment is rejected", "https://ntfy.example.com#x", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Notify.Ntfy.URL = tt.url

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected notify.ntfy.url %q to be rejected", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected notify.ntfy.url %q to be accepted, got %v", tt.url, err)
			}
		})
	}
}

func TestValidate_NtfyTopic(t *testing.T) {
	tests := []struct {
		name    string
		topic   string
		wantErr bool
	}{
		{"a plain topic is accepted", "job-matches", false},
		{"underscores are accepted", "job_matches", false},
		{"mixed case is accepted", "JobMatches", false},
		{"digits are accepted", "job-matches-2", false},
		{"a slash is rejected - it would address a different endpoint", "a/b", true},
		{"a query character is rejected", "a?b", true},
		{"a fragment character is rejected", "a#b", true},
		{"a space is rejected", "job matches", true},
		{"an empty topic is rejected", "", true},
		{"a path traversal attempt is rejected", "../../admin", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Notify.Ntfy.Topic = tt.topic

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected notify.ntfy.topic %q to be rejected", tt.topic)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected notify.ntfy.topic %q to be accepted, got %v", tt.topic, err)
			}
		})
	}
}

func TestValidate_SourceSlugs(t *testing.T) {
	tests := []struct {
		name    string
		company string
		wantErr bool
	}{
		{"a normal board token is accepted", "grafanalabs", false},
		{"dot dash and underscore are accepted", "grafana-labs_co.uk", false},
		{"a slash is rejected - it would add a path segment", "acme/../admin", true},
		{"a query character is rejected", "acme?x=1", true},
		{"a fragment character is rejected", "acme#x", true},
		{"a space is rejected", "acme inc", true},
		{"an empty slug is rejected", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Sources[0].Company = tt.company

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected company %q to be rejected", tt.company)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected company %q to be accepted, got %v", tt.company, err)
			}
		})
	}
}

func TestValidate_WorkdayHost(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"a bare hostname is accepted", "atlassian.wd3.myworkdayjobs.com", false},
		{"a hyphenated label is accepted", "my-tenant.wd1.myworkdayjobs.com", false},
		{"a scheme is rejected", "https://atlassian.wd3.myworkdayjobs.com", true},
		{"a port is rejected", "atlassian.wd3.myworkdayjobs.com:8443", true},
		{"a path is rejected", "atlassian.wd3.myworkdayjobs.com/foo", true},
		{"a single label is rejected", "myworkdayjobs", true},
		{"whitespace is rejected", "atlassian.wd3.myworkdayjobs.com ", true},
		{"a newline is rejected", "atlassian.wd3.myworkdayjobs.com\nx", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Sources[0] = SourceConfig{
				Type:        "workday",
				DisplayName: "Atlassian",
				Tenant:      "atlassian",
				Host:        tt.host,
				Site:        "Atlassian",
			}

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected host %q to be rejected", tt.host)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected host %q to be accepted, got %v", tt.host, err)
			}
		})
	}
}

func TestValidate_WorkdayTenantAndSite(t *testing.T) {
	tests := []struct {
		name    string
		tenant  string
		site    string
		wantErr bool
	}{
		{"typical values are accepted", "atlassian", "Atlassian", false},
		{"a slash in the tenant is rejected", "atlassian/x", "Atlassian", true},
		{"a slash in the site is rejected", "atlassian", "Atlassian/x", true},
		{"a query in the site is rejected", "atlassian", "Atlassian?x=1", true},
		{"whitespace in the site is rejected", "atlassian", "Atlassian Site", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Sources[0] = SourceConfig{
				Type:        "workday",
				DisplayName: "Atlassian",
				Tenant:      tt.tenant,
				Host:        "atlassian.wd3.myworkdayjobs.com",
				Site:        tt.site,
			}

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected tenant %q / site %q to be rejected", tt.tenant, tt.site)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected tenant %q / site %q to be accepted, got %v", tt.tenant, tt.site, err)
			}
		})
	}
}

func TestValidate_EnvVarNames(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		wantErr bool
	}{
		{"DEEPSEEK_API_KEY", "ai", false},
		{"_lower_UPPER_2", "ai", false},
		{"2LEADING_DIGIT", "ai", true},
		{"has-a-dash", "ai", true},
		{"has a space", "ai", true},
		{"NESTED", "ntfy", false},
		{"UPPER_CASE", "ntfy", false},
		{"lower_case", "ntfy", false},
		{"NESTED.WITH.DOTS", "ntfy", true},
		{"", "ntfy", false}, // tokenEnv is optional, so empty is fine
	}

	for _, tt := range tests {
		t.Run(tt.field+"_"+tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			if tt.field == "ai" {
				cfg.AI.APIKeyEnv = tt.name
			} else {
				cfg.Notify.Ntfy.TokenEnv = tt.name
			}

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected %q to be rejected", tt.name)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected %q to be accepted, got %v", tt.name, err)
			}
		})
	}
}

// The example config shipped in the repo must keep loading: it is the one
// config a new operator copies, so a validation rule that rejects it would
// be a broken first run.
func TestLoad_ExampleConfigPassesStricterValidation(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config", "config.example.yaml"))
	if err != nil {
		t.Fatalf("the shipped example config must load: %v", err)
	}
	if len(cfg.Sources) == 0 {
		t.Fatal("expected the example config to define at least one source")
	}
}

// TestLoad_DeployExampleConfigMap guards the other config shipped in the
// repo. deploy/configmap.example.yaml wraps its document in a ConfigMap, so
// nothing parses it as a config unless a test does - and a schema change that
// broke only this file would otherwise surface as a CronJob exiting 1 in a
// real cluster, at 06:00, silently.
func TestLoad_DeployExampleConfigMap(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", "configmap.example.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // fixed path inside the repo
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var manifest struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	embedded, ok := manifest.Data["config.yaml"]
	if !ok {
		t.Fatalf("%s defines no data[\"config.yaml\"]", path)
	}

	cfg, err := Load(writeTempConfig(t, embedded))
	if err != nil {
		t.Fatalf("the config embedded in %s must load cleanly: %v", path, err)
	}
	if len(cfg.Sources) == 0 {
		t.Fatal("expected the embedded config to define at least one source")
	}
}

// TestValidate_MatchTiers covers the mistakes that are otherwise invisible at
// runtime. Each one fails silently in a different way: a descending list makes
// the later tiers dead, a missing catch-all leaves a notified match with no
// emoji at all, and a priority of 0 is not on ntfy's 1-5 scale. None of them
// produce an error or a log line once the CronJob is running - just
// notifications that look subtly wrong.
func TestValidate_MatchTiers(t *testing.T) {
	score := func(f float64) *float64 { return &f }

	tests := []struct {
		name    string
		tiers   []MatchTier
		wantErr bool
	}{
		{
			name:  "the defaults are valid",
			tiers: DefaultMatchTiers(),
		},
		{
			name: "a well-formed custom list is accepted",
			tiers: []MatchTier{
				{MinScore: score(0.9), Emoji: "🔥", Priority: 5},
				{Emoji: "📋", Priority: 2},
			},
		},
		{
			name: "an ascending list is rejected",
			tiers: []MatchTier{
				{MinScore: score(0.5), Emoji: "⭐", Priority: 4},
				{MinScore: score(0.8), Emoji: "💎", Priority: 5},
				{Emoji: "💼", Priority: 3},
			},
			wantErr: true,
		},
		{
			name: "two tiers sharing a boundary are rejected",
			tiers: []MatchTier{
				{MinScore: score(0.8), Emoji: "⭐", Priority: 4},
				{MinScore: score(0.8), Emoji: "💎", Priority: 5},
				{Emoji: "💼", Priority: 3},
			},
			wantErr: true,
		},
		{
			name: "a list with no catch-all is rejected",
			tiers: []MatchTier{
				{MinScore: score(0.9), Emoji: "💎", Priority: 5},
				{MinScore: score(0.8), Emoji: "⭐", Priority: 4},
			},
			wantErr: true,
		},
		{
			name:    "an empty emoji is rejected",
			tiers:   []MatchTier{{Emoji: "   ", Priority: 3}},
			wantErr: true,
		},
		{
			name:    "an over-long emoji is rejected",
			tiers:   []MatchTier{{Emoji: strings.Repeat("x", maxEmojiRunes+1), Priority: 3}},
			wantErr: true,
		},
		{
			name:    "priority 0 is rejected",
			tiers:   []MatchTier{{Emoji: "💼", Priority: 0}},
			wantErr: true,
		},
		{
			name:    "priority 6 is rejected",
			tiers:   []MatchTier{{Emoji: "💼", Priority: 6}},
			wantErr: true,
		},
		{
			name:    "a minScore above 1 is rejected",
			tiers:   []MatchTier{{MinScore: score(1.5), Emoji: "💼", Priority: 3}},
			wantErr: true,
		},
		{
			name: "more tiers than a phone can distinguish is rejected",
			tiers: func() []MatchTier {
				tiers := make([]MatchTier, 0, maxMatchTiers+1)
				for i := 0; i < maxMatchTiers; i++ {
					tiers = append(tiers, MatchTier{MinScore: score(1 - float64(i)/100), Emoji: "⭐", Priority: 3})
				}
				return append(tiers, MatchTier{Emoji: "💼", Priority: 3})
			}(),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Notify.Ntfy.MatchTiers = tt.tiers

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected matchTiers to be rejected")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected matchTiers to be accepted, got %v", err)
			}
		})
	}
}

// TestMatchTiers_DefaultsSeededWhenOmitted keeps every existing config working:
// matchTiers is new and optional, so a config that predates it has to end up
// with the defaults rather than with an empty list and no emoji.
func TestMatchTiers_DefaultsSeededWhenOmitted(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Notify.Ntfy.MatchTiers = nil
	cfg.applyDefaults()

	if len(cfg.Notify.Ntfy.MatchTiers) == 0 {
		t.Fatal("expected the default matchTiers to be seeded")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected the seeded defaults to validate, got %v", err)
	}
}

// TestUnreachableTiers pins the case that is a config mistake rather than a
// validation error: a tier whose boundary sits below minAIScore can never be
// reached, because a posting below the threshold is never notified at all. It
// is a warning rather than a failure - the operator may be about to lower the
// threshold - but it has to be reported, since the only other symptom is an
// emoji that never arrives.
func TestUnreachableTiers(t *testing.T) {
	score := func(f float64) *float64 { return &f }
	ntfy := NtfyConfig{MatchTiers: []MatchTier{
		{MinScore: score(0.95), Emoji: "💎", Priority: 5},
		{MinScore: score(0.85), Emoji: "⭐", Priority: 4},
		{MinScore: score(0.4), Emoji: "👀", Priority: 2},
		{Emoji: "💼", Priority: 3},
	}}

	got := ntfy.UnreachableTiers(0.6)
	if len(got) != 1 || got[0].Emoji != "👀" {
		t.Fatalf("UnreachableTiers(0.6) = %v, want just the 0.4 tier", got)
	}
	if got := ntfy.UnreachableTiers(0.1); len(got) != 0 {
		t.Fatalf("UnreachableTiers(0.1) = %v, want none", got)
	}
	// The catch-all has no boundary, so it is reachable at any threshold.
	for _, t2 := range ntfy.UnreachableTiers(1.0) {
		if t2.MinScore == nil {
			t.Error("the catch-all tier must never be reported as unreachable")
		}
	}
}
