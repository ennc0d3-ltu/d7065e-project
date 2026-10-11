// responder turns the detector's fire decisions into a dispatch for the
// (mock) emergency responders: affected rooms, who is in danger or at risk,
// and a route out that avoids burning rooms. It only reads BuildSim.
//
// Configuration (environment):
//
//	MQTT_URL      broker URL                          (default tcp://127.0.0.1:1883)
//	BUILDSIM_URL  BuildSim base URL                   (default http://127.0.0.1:9090)
//	SAFE_ROOMS    comma list of level/room to route to (required)
//	DOORS         comma list roomA|roomB|openness, used only for adjacency
//	MIN_INTERVAL  least sim seconds between non-urgent updates (default 30)
//	ACK_DELAY     sim seconds the mock responder takes to acknowledge (default 10)
//	LISTEN        health endpoint                     (default :8096)
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/responder"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "responder")

	safe := splitList(os.Getenv("SAFE_ROOMS"))
	if len(safe) == 0 {
		log.Error("SAFE_ROOMS is required, for example SAFE_ROOMS=level0/A101")
		os.Exit(2)
	}
	minInterval := number(log, "MIN_INTERVAL", 30)
	ackDelay := number(log, "ACK_DELAY", 10)

	adjacent := map[string][]string{}
	for _, spec := range splitList(os.Getenv("DOORS")) {
		f := strings.Split(spec, "|")
		if len(f) < 2 {
			log.Error("bad DOORS entry", "entry", spec)
			os.Exit(2)
		}
		adjacent[f[0]] = append(adjacent[f[0]], f[1])
		adjacent[f[1]] = append(adjacent[f[1]], f[0])
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	h := health.New("responder")
	mux := http.NewServeMux()
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	srv := &http.Server{Addr: orDefault(os.Getenv("LISTEN"), ":8096"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	mqttURL := orDefault(os.Getenv("MQTT_URL"), "tcp://127.0.0.1:1883")
	// A persistent session, so decisions published while this process was down
	// are delivered on reconnect. They can arrive before the service below
	// exists, so the handlers only queue them; a goroutine applies them later.
	type msg struct {
		topic   string
		payload []byte
		beat    bool
	}
	inbox := make(chan msg, 10000)
	conn, err := mqttx.ConnectWith(ctx, mqttURL, "responder", mqttx.Options{
		Persistent: true,
		Subscriptions: []mqttx.Subscription{
			{Filter: "decisions/#", QoS: 1, Handler: func(t string, p []byte) { inbox <- msg{topic: t, payload: p} }},
			// The detector's heartbeat carries sim time once per decision
			// period: it is this service's clock.
			{Filter: proto.HeartbeatTopic("detector"), QoS: 0, Handler: func(t string, p []byte) { inbox <- msg{topic: t, payload: p, beat: true} }},
		},
	})
	if err != nil {
		log.Error("mqtt connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())
	go mqttx.PublishHeartbeats(ctx, conn, "responder", 2*time.Second, nil)
	q, err := mqttx.StartQueueing(ctx, conn, "responder")
	if err != nil {
		log.Error("outbox", "err", err)
		os.Exit(1)
	}
	if n := q.Outbox().Len(); n > 0 {
		log.Info("replaying messages left from the previous run", "count", n)
	}

	bs := buildsim.New(orDefault(os.Getenv("BUILDSIM_URL"), "http://127.0.0.1:9090"), 2*time.Second)
	svc := responder.New(responder.Config{Safe: safe, Adjacent: adjacent, MinInterval: minInterval, AckDelay: ackDelay}, bs, bs, q)
	svc.WithEntities(bs) // who is walking out of an affected room

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-inbox:
				if m.beat {
					var hb proto.Heartbeat
					if json.Unmarshal(m.payload, &hb) == nil {
						svc.Tick(ctx, hb.SimTime)
						h.Beat()
					}
					continue
				}
				if err := svc.HandleDecision(ctx, m.topic, m.payload); err != nil {
					log.Warn("decision rejected", "topic", m.topic, "err", err)
					q.DeadLetter(ctx, m.topic, m.payload, err)
				}
			}
		}
	}()
	h.SetReady(true)
	log.Info("started", "mqtt", mqttURL, "safe_rooms", safe)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Info("stopped", "acks", len(svc.Acks()))
}

func number(log *slog.Logger, name string, def float64) float64 {
	v, err := strconv.ParseFloat(orDefault(os.Getenv(name), strconv.FormatFloat(def, 'f', -1, 64)), 64)
	if err != nil || v < 0 {
		log.Error("bad number", "name", name)
		os.Exit(2)
	}
	return v
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
