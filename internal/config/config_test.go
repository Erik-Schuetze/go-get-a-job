package config

import (
	"os"
	"path/filepath"
	"testing"
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
  locations: ["Germany", "Remote"]
  minAIScore: 0.8

ai:
  apiKeyEnv: DEEPSEEK_API_KEY
  profile: "Looking for platform engineering roles."

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
