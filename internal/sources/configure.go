package sources

import (
	"fmt"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
)

// ConfigureFromConfig applies the outbound-request settings from a loaded
// config to the shared client every connector uses.
//
// It exists so the operator's guard settings are not decorative: a politeness
// floor that cannot be raised from config is a comment, not a limit. main
// calls it once at startup, before BuildAll; afterwards the client already
// exists and further calls have no effect (see ConfigureClient).
//
// The interval is validated here rather than trusted, because multiplying a
// milliseconds field by time.Millisecond silently wraps for absurd inputs -
// turning a deliberate slowdown into no delay at all, which is the one
// failure mode of this setting that would actually be harmful.
func ConfigureFromConfig(cfg config.Config) error {
	ms := cfg.Guard.MinRequestIntervalMs
	const maxMs = int64(24 * 60 * 60 * 1000)
	if int64(ms) > maxMs {
		return fmt.Errorf("guard.minRequestIntervalMs: %d ms is more than a day; this looks like a unit mistake", ms)
	}

	ConfigureClient(time.Duration(ms)*time.Millisecond, cfg.Guard.MaxRequestsPerRun)
	return nil
}
