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
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("loading config failed", "error", err)
		return 1
	}

	apiKey := os.Getenv(cfg.AI.APIKeyEnv)
	if apiKey == "" {
		logger.Error("AI API key not set", "env_var", cfg.AI.APIKeyEnv)
		return 1
	}

	src, err := sources.BuildAll(cfg.Sources)
	if err != nil {
		logger.Error("building sources failed", "error", err)
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
	notifier := notify.NewNtfy(cfg.Notify.Ntfy.URL, cfg.Notify.Ntfy.Topic, ntfyToken)

	scorer := filter.NewAIScorer(cfg.AI.BaseURL, apiKey, cfg.AI.Model)

	r := &runner.Runner{
		Sources:  src,
		Filter:   cfg.Filter,
		Profile:  cfg.AI.Profile,
		Scorer:   scorer,
		Store:    st,
		Notifier: notifier,
		Logger:   logger,
	}

	summary := r.Run(ctx)

	if summary.AllSourcesFailed(len(src)) {
		runErr := errors.Join(summary.SourceErrors...)
		logger.Error("every source failed to fetch; this run found nothing", "error", runErr)
		if notifyErr := notifier.NotifyFailure(ctx, fmt.Errorf("go-get-a-job: all %d source(s) failed: %w", len(src), runErr)); notifyErr != nil {
			logger.Error("also failed to send failure notification", "error", notifyErr)
		}
		return 1
	}

	return 0
}
