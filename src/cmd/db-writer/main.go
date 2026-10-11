// db-writer stores readings, decisions and commands from MQTT in TimescaleDB
// (decision D1). It is off the safety path: it starts and keeps running while
// the database is down, buffering what it can and shedding old readings before
// the audit trail.
//
// Configuration (environment):
//
//	MQTT_URL      broker URL                  (default tcp://127.0.0.1:1883)
//	DATABASE_URL  libpq URL (default postgres://fire@127.0.0.1:55432/fire?sslmode=disable)
//	FLUSH_MS      flush interval in ms        (default 1000)
//	BATCH         readings per write          (default 500)
//	MAX_BUFFERED  records held while the database is down (default 50000)
//	LISTEN        health and /stats           (default :8097)
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

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/dbwriter"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "db-writer")

	flushMS := positive(log, "FLUSH_MS", 1000)
	batch := positive(log, "BATCH", 500)
	maxBuffered := positive(log, "MAX_BUFFERED", 50000)
	dbURL := orDefault(os.Getenv("DATABASE_URL"), "postgres://fire@127.0.0.1:55432/fire?sslmode=disable")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st := store.NewReconnecting(func(ctx context.Context) (dbwriter.Store, error) {
		octx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		pg, err := store.Open(octx, dbURL)
		if err != nil {
			return nil, err
		}
		if err := pg.Migrate(octx); err != nil {
			pg.Close()
			return nil, err
		}
		log.Info("database connected and migrated")
		return pg, nil
	}, 5*time.Second)
	w := dbwriter.New(st, dbwriter.Config{BatchSize: batch, MaxBuffered: maxBuffered})

	h := health.New("db-writer")
	mux := http.NewServeMux()
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	mux.HandleFunc("/stats", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(w.Stats())
	})
	srv := &http.Server{Addr: orDefault(os.Getenv("LISTEN"), ":8097"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	mqttURL := orDefault(os.Getenv("MQTT_URL"), "tcp://127.0.0.1:1883")
	// A persistent session: while this process is down the broker queues what
	// it misses, and delivers it on reconnect. Handlers are registered at
	// connect time so that backlog is not lost.
	handle := func(topic string, payload []byte) {
		if err := w.Handle(ctx, topic, payload); err != nil {
			log.Warn("message rejected", "topic", topic, "err", err)
		}
	}
	var subs []mqttx.Subscription
	for _, filter := range []string{"sensors/#", "decisions/#", "commands/sprinkler/#", "replay/#", "dlq/#", "dispatch/#", "heartbeat/#"} {
		subs = append(subs, mqttx.Subscription{Filter: filter, QoS: 1, Handler: handle})
	}
	conn, err := mqttx.ConnectWith(ctx, mqttURL, "db-writer", mqttx.Options{Persistent: true, Subscriptions: subs})
	if err != nil {
		log.Error("mqtt connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())
	go mqttx.PublishHeartbeats(ctx, conn, "db-writer", 2*time.Second, nil)
	h.SetReady(true)
	log.Info("started", "mqtt", mqttURL, "flush_ms", flushMS)

	tick := time.NewTicker(time.Duration(flushMS) * time.Millisecond)
	defer tick.Stop()
	var failing bool
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = w.Flush(fctx) // a last try; whatever is left is lost on exit
			cancel()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			log.Info("stopped", "stats", w.Stats())
			return
		case <-tick.C:
			err := w.Flush(ctx)
			h.Beat()
			// log the change of state, not every failed flush
			switch {
			case err != nil && !failing:
				log.Warn("database not accepting writes: buffering", "err", err)
				failing = true
			case err == nil && failing:
				log.Info("database writes recovered", "stats", w.Stats())
				failing = false
			}
		}
	}
}

func positive(log *slog.Logger, name string, def int) int {
	v, err := strconv.Atoi(orDefault(os.Getenv(name), strconv.Itoa(def)))
	if err != nil || v <= 0 {
		log.Error("must be a positive integer", "name", name)
		os.Exit(2)
	}
	return v
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
