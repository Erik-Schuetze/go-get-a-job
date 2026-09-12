package sources

import (
	"fmt"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
)

// New builds a Source for a single config entry.
func New(cfg config.SourceConfig) (Source, error) {
	switch cfg.Type {
	case "greenhouse":
		return NewGreenhouse(cfg.Company, cfg.DisplayName), nil
	case "lever":
		return NewLever(cfg.Company, cfg.DisplayName), nil
	case "ashby":
		return NewAshby(cfg.Company, cfg.DisplayName), nil
	case "smartrecruiters":
		return NewSmartRecruiters(cfg.Company, cfg.DisplayName), nil
	case "workday":
		return NewWorkday(cfg.Tenant, cfg.Host, cfg.Site, cfg.DisplayName), nil
	default:
		return nil, fmt.Errorf("unknown source type %q", cfg.Type)
	}
}

// BuildAll builds a Source for every config entry, in order. It fails
// fast (returns an error, builds nothing) if any single entry is
// misconfigured, rather than silently skipping it - a typo in a config
// entry should be caught at startup, not by an operator noticing they
// stopped getting notifications for one company.
func BuildAll(cfgs []config.SourceConfig) ([]Source, error) {
	result := make([]Source, 0, len(cfgs))
	for i, c := range cfgs {
		s, err := New(c)
		if err != nil {
			return nil, fmt.Errorf("sources[%d] (%s): %w", i, c.DisplayName, err)
		}
		result = append(result, s)
	}
	return result, nil
}
