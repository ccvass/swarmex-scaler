package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/client"

	"github.com/ccvass/swarmex/swarmex-scaler"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	promURL := os.Getenv("PROMETHEUS_URL")
	if promURL == "" {
		promURL = "http://prometheus:9090"
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		logger.Error("failed to create Docker client", "error", err)
		os.Exit(1)
	}
	defer cli.Close()

	sc := scaler.New(cli, promURL, logger)

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "ok")
		})
		logger.Info("health endpoint", "addr", ":8080")
		http.ListenAndServe(":8080", nil)
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("swarmex-scaler starting", "prometheus", promURL)

	// Scaling evaluation loop
	go sc.RunLoop(ctx, 15*time.Second)

	// Docker event listener for service discovery
	msgCh, errCh := cli.Events(ctx, events.ListOptions{})
	for {
		select {
		case event := <-msgCh:
			sc.HandleEvent(ctx, event)
		case err := <-errCh:
			if ctx.Err() != nil {
				logger.Info("shutdown complete")
				return
			}
			logger.Error("event stream error", "error", err)
			return
		case <-ctx.Done():
			logger.Info("shutdown complete")
			return
		}
	}
}
