// bridge subscribes to sensor readings on MQTT and writes them into BuildSim,
// dropping duplicates and counting gaps (decision D1, NFR12).
//
// Configuration (environment):
//
//	MQTT_URL     broker URL        (default tcp://127.0.0.1:1883)
//	BUILDSIM_URL BuildSim base URL (default http://127.0.0.1:9090)
//	LISTEN       health + /stats   (default :8092)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/bridge"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "bridge")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	b := bridge.New(buildsim.New(orDefault(os.Getenv("BUILDSIM_URL"), "http://127.0.0.1:9090"), 2*time.Second))
	h := health.New("bridge")

	mux := http.NewServeMux()
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(b.Stats())
	})
	srv := &http.Server{Addr: orDefault(os.Getenv("LISTEN"), ":8092"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	mqttURL := orDefault(os.Getenv("MQTT_URL"), "tcp://127.0.0.1:1883")
	conn, err := mqttx.Connect(ctx, mqttURL, "bridge")
	if err != nil {
		log.Error("mqtt connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())
	go mqttx.PublishHeartbeats(ctx, conn, "bridge", 2*time.Second, nil)
	q, err := mqttx.StartQueueing(ctx, conn, "bridge")
	if err != nil {
		log.Error("outbox", "err", err)
		os.Exit(1)
	}
	if n := q.Outbox().Len(); n > 0 {
		log.Info("replaying messages left from the previous run", "count", n)
	}

	handler := func(topic string, payload []byte) {
		hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := b.Handle(hctx, topic, payload); err != nil {
			log.Warn("reading not applied", "topic", topic, "err", err)
			if errors.Is(err, bridge.ErrMalformed) {
				q.DeadLetter(ctx, topic, payload, err)
			}
		}
		h.Beat()
	}
	if err := conn.Subscribe(ctx, "sensors/#", 1, handler); err != nil {
		log.Error("subscribe", "err", err)
		os.Exit(1)
	}
	h.SetReady(true)
	log.Info("started", "mqtt", mqttURL)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Info("stopped", "stats", b.Stats())
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
