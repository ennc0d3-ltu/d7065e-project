// scenario-console is the demo's control panel: pick a room, set it on fire
// (or start cooking or dust), clear the scenario, and act as the operator. It
// only forwards a fixed list of requests to physics-sim and the detector.
//
// Configuration (environment):
//
//	PHYSICS_URL   physics-sim base URL   (default http://127.0.0.1:8090)
//	DETECTOR_URL  detector base URL      (default http://127.0.0.1:8093)
//	VIEWER_URL    link shown on the page (optional)
//	GRAFANA_URL   link shown on the page (optional)
//	LISTEN        address                (default :8099)
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/console"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "scenario-console")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	h := health.New("scenario-console")
	h.SetReady(true)
	mux := http.NewServeMux()
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	mux.Handle("/", console.Handler(
		orDefault(os.Getenv("PHYSICS_URL"), "http://127.0.0.1:8090"),
		orDefault(os.Getenv("DETECTOR_URL"), "http://127.0.0.1:8093"),
		console.Links{Viewer: os.Getenv("VIEWER_URL"), Grafana: os.Getenv("GRAFANA_URL")},
	))
	srv := &http.Server{Addr: orDefault(os.Getenv("LISTEN"), ":8099"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()
	log.Info("started", "listen", srv.Addr)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Info("stopped")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
