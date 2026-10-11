// detector reads sensor readings from MQTT, decides per room whether there is
// a fire, and publishes decisions and sprinkler commands (decisions D3, D4).
//
// Configuration (environment):
//
//	MQTT_URL    broker URL                       (default tcp://127.0.0.1:1883)
//	MODEL_PATH  trained model from cmd/trainer   (empty: rule baseline only)
//	LISTEN      health, operator and status API  (default :8093)
//	PERIOD      decision period in sim seconds   (default 5)
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detectsvc"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "detector")

	period, err := strconv.ParseFloat(orDefault(os.Getenv("PERIOD"), "5"), 64)
	if err != nil || period <= 0 {
		log.Error("PERIOD must be a positive number")
		os.Exit(2)
	}

	var model *detector.Model
	if path := os.Getenv("MODEL_PATH"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Error("cannot read the model", "path", path, "err", err)
			os.Exit(1)
		}
		model = new(detector.Model)
		if err := json.Unmarshal(raw, model); err != nil {
			log.Error("cannot load the model", "path", path, "err", err)
			os.Exit(1)
		}
		log.Info("model loaded", "path", path)
	} else {
		log.Warn("no MODEL_PATH: running the rule baseline only")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mqttURL := orDefault(os.Getenv("MQTT_URL"), "tcp://127.0.0.1:1883")
	conn, err := mqttx.Connect(ctx, mqttURL, "detector")
	if err != nil {
		log.Error("mqtt connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())

	q, err := mqttx.StartQueueing(ctx, conn, "detector")
	if err != nil {
		log.Error("outbox", "err", err)
		os.Exit(1)
	}
	if n := q.Outbox().Len(); n > 0 {
		log.Info("replaying messages left from the previous run", "count", n)
	}
	svc := detectsvc.New(detectsvc.DefaultConfig(), model, detector.DefaultThresholds(), q)
	if err := conn.Subscribe(ctx, "sensors/#", 1, func(topic string, payload []byte) {
		if err := svc.Handle(ctx, topic, payload); err != nil {
			log.Warn("reading rejected", "topic", topic, "err", err)
			q.DeadLetter(ctx, topic, payload, err)
		}
	}); err != nil {
		log.Error("subscribe", "err", err)
		os.Exit(1)
	}

	// Tuned thresholds and models come from the registry, but the detector never
	// waits for it: it starts on its defaults (or its cached bundle) and polls.
	if url := os.Getenv("REGISTRY_URL"); url != "" {
		loader := &detectsvc.Loader{
			URL: url, CachePath: orDefault(os.Getenv("BUNDLE_CACHE"), "/data/outbox/bundle-cache.json"),
			Applier: svc, Log: log,
		}
		if loader.LoadCache() {
			log.Info("started from the cached bundle", "version", loader.Version())
		}
		every, _ := time.ParseDuration(orDefault(os.Getenv("REGISTRY_POLL"), "30s"))
		if every <= 0 {
			every = 30 * time.Second
		}
		go loader.Run(ctx, every)
	}

	h := health.New("detector")
	mux := http.NewServeMux()
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	mux.Handle("/", svc.OperatorHandler())
	srv := &http.Server{Addr: orDefault(os.Getenv("LISTEN"), ":8093"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()
	h.SetReady(true)
	log.Info("started", "mqtt", mqttURL, "period_sim_s", period, "model", model != nil)

	// Decide once per `period` of sim time, driven by the readings' own clock.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	last := -period
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			log.Info("stopped")
			return
		case <-tick.C:
			if now := svc.SimNow(); now > 0 && now >= last+period {
				svc.Tick(ctx, now)
				last = now
				h.Beat()
			}
		}
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
