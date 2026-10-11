// registry serves versioned detector bundles (thresholds, optional model,
// evaluation report) and says which one is active. Publishing never activates
// a bundle: promotion and rollback name a human approver.
//
// Configuration (environment):
//
//	REGISTRY_FILE  where the registry keeps its state  (default /data/registry/registry.json)
//	LISTEN         address                             (default :8100)
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/registry"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "registry")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	path := os.Getenv("REGISTRY_FILE")
	if path == "" {
		path = "/data/registry/registry.json"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Error("cannot create the registry directory", "err", err)
		os.Exit(1)
	}
	st, err := registry.OpenStore(path)
	if err != nil {
		log.Error("cannot open the registry", "err", err)
		os.Exit(1)
	}

	h := health.New("registry")
	h.SetReady(true)
	mux := http.NewServeMux()
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	mux.Handle("/", registry.Handler(st))
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8100"
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()
	log.Info("started", "listen", addr, "file", path)
	<-ctx.Done()
	sc, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(sc)
}
