package sources

import (
	"testing"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
)

func TestNew_AllKnownTypes(t *testing.T) {
	cases := []struct {
		cfg      config.SourceConfig
		wantName string
	}{
		{config.SourceConfig{Type: "greenhouse", Company: "acme", DisplayName: "Acme"}, "greenhouse"},
		{config.SourceConfig{Type: "lever", Company: "acme", DisplayName: "Acme"}, "lever"},
		{config.SourceConfig{Type: "ashby", Company: "acme", DisplayName: "Acme"}, "ashby"},
		{config.SourceConfig{Type: "smartrecruiters", Company: "acme", DisplayName: "Acme"}, "smartrecruiters"},
		{config.SourceConfig{Type: "workday", Tenant: "acme", Host: "acme.wd1.myworkdayjobs.com", Site: "Acme", DisplayName: "Acme"}, "workday"},
	}

	for _, c := range cases {
		s, err := New(c.cfg)
		if err != nil {
			t.Fatalf("New(%+v) returned error: %v", c.cfg, err)
		}
		if s.Name() != c.wantName {
			t.Errorf("New(%+v).Name() = %q, want %q", c.cfg, s.Name(), c.wantName)
		}
	}
}

func TestNew_UnknownType(t *testing.T) {
	if _, err := New(config.SourceConfig{Type: "bogus"}); err == nil {
		t.Fatal("expected error for unknown source type, got nil")
	}
}

func TestBuildAll_Success(t *testing.T) {
	cfgs := []config.SourceConfig{
		{Type: "greenhouse", Company: "acme", DisplayName: "Acme"},
		{Type: "lever", Company: "beta", DisplayName: "Beta"},
	}
	built, err := BuildAll(cfgs)
	if err != nil {
		t.Fatalf("BuildAll returned error: %v", err)
	}
	if len(built) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(built))
	}
}

func TestBuildAll_FailsFastOnBadEntry(t *testing.T) {
	cfgs := []config.SourceConfig{
		{Type: "greenhouse", Company: "acme", DisplayName: "Acme"},
		{Type: "bogus", DisplayName: "Bad"},
	}
	if _, err := BuildAll(cfgs); err == nil {
		t.Fatal("expected error when one entry is misconfigured, got nil")
	}
}
