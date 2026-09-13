// Command go-get-a-job runs a single fetch-filter-score-notify pass over all
// configured job sources, then exits. It's designed to be invoked
// periodically (e.g. by a Kubernetes CronJob), not run as a long-lived
// server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/filter"
	"github.com/Erik-Schuetze/go-get-a-job/internal/notify"
	"github.com/Erik-Schuetze/go-get-a-job/internal/runner"
	"github.com/Erik-Schuetze/go-get-a-job/internal/sources"
	"github.com/Erik-Schuetze/go-get-a-job/internal/store"
)

func main() {
	os.Exit(run())
}

// run contains the actual program logic and returns a process exit code,
// keeping main() itself trivial and untestable-code-free.
func run() int {
	configPath := flag.String("config", "config.yaml", "path to the go-get-a-job YAML config file")
	logLevel := flag.String("log-level", "info", "log verbosity: debug, info, warn, or error")
	validate := flag.Bool("validate", false, "check every configured source is reachable and returns usable postings, then exit without scoring or notifying")
	flag.Parse()

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -log-level: %v\n", err)
		return 2
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("loading config failed", "error", err)
		return 1
	}

	if err := sources.ConfigureFromConfig(*cfg); err != nil {
		logger.Error("configuring outbound HTTP client failed", "error", err)
		return 1
	}

	src, err := sources.BuildAll(cfg.Sources)
	if err != nil {
		logger.Error("building sources failed", "error", err)
		return 1
	}

	if *validate {
		if !sources.Validate(ctx, logger, src) {
			return 1
		}
		return 0
	}

	apiKey := os.Getenv(cfg.AI.APIKeyEnv)
	if apiKey == "" {
		logger.Error("AI API key not set", "env_var", cfg.AI.APIKeyEnv)
		return 1
	}

	st, err := store.OpenSQLite(ctx, cfg.Store.Path)
	if err != nil {
		logger.Error("opening store failed", "error", err)
		return 1
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("closing store failed", "error", err)
		}
	}()

	var ntfyToken string
	if cfg.Notify.Ntfy.TokenEnv != "" {
		ntfyToken = os.Getenv(cfg.Notify.Ntfy.TokenEnv)
	}
	notifier := notify.NewNtfy(cfg.Notify.Ntfy.URL, cfg.Notify.Ntfy.Topic, ntfyToken, cfg.Notify.Ntfy.MatchTiers)

	// A tier that no reachable score can land in is a config mistake that is
	// otherwise invisible: the emoji simply never appears, and there is no
	// evidence pointing at why. Report it once, at startup.
	if unreachable := cfg.Notify.Ntfy.UnreachableTiers(cfg.Filter.MinAIScore); len(unreachable) > 0 {
		logger.Warn("notification tiers unreachable at the configured minAIScore",
			"min_score", cfg.Filter.MinAIScore,
			"tiers", unreachable,
		)
	}

	scorer := filter.NewAIScorer(cfg.AI.BaseURL, apiKey, cfg.AI.Model)
	scorer.Instructions = cfg.AI.Instructions

	r := &runner.Runner{
		Sources:  src,
		Filter:   cfg.Filter,
		Profile:  cfg.AI.Profile,
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
		Guard:    cfg.Guard,
		Logger:   logger,
	}

	summary := r.Run(ctx)

	// Any error means part of the pipeline didn't run, so the exit code has
	// to be nonzero for all of them, not just the total-failure case. The
	// CronJob's success/failure history is the only signal that survives the
	// logs being rotated away, so it must not report success for a run that
	// quietly lost half its sources.
	if len(summary.SourceErrors) > 0 || len(summary.ProcessErrors) > 0 {
		runErr := errors.Join(append(append([]error{}, summary.SourceErrors...), summary.ProcessErrors...)...)
		logger.Error("run finished with errors",
			"source_errors", len(summary.SourceErrors),
			"process_errors", len(summary.ProcessErrors),
		)
		// Only a total source wipe-out is worth waking the operator for
		// immediately; partial failures are already visible as an error
		// exit code, and a notification per flaky board would be noise.
		if summary.AllSourcesFailed(len(src)) {
			if notifyErr := notifier.NotifyFailure(ctx, fmt.Errorf("go-get-a-job: all %d source(s) failed: %w", len(src), runErr)); notifyErr != nil {
				logger.Error("also failed to send failure notification", "error", notifyErr)
			}
		}
		return 1
	}

	return 0
}

// parseLogLevel maps the -log-level flag onto a slog.Level.
func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%q is not one of debug, info, warn, error", s)
	}
}
