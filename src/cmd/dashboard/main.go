// dashboard draws the system's decisions on BuildSim's 3D viewer: a fire-risk
// heatmap, the evacuation route, fire / warning / sprinkler / water effects,
// alert cards, and which components are down. It is the only writer of
// BuildSim's room layers, effects and alerts (each PUT replaces the whole
// collection).
//
// Configuration (environment):
//
//	MQTT_URL       broker URL                      (default tcp://127.0.0.1:1883)
//	BUILDSIM_URL   BuildSim base URL               (default http://127.0.0.1:9090)
//	EXPECTED       comma list of processes that must send heartbeats
//	PUBLISH_MS     overlay refresh in ms           (default 1000)
//	COMPONENT_TTL  seconds without a heartbeat before a component counts as down (default 10)
//	LISTEN         health endpoint                 (default :8095)
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
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/dashboard"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

const defaultExpected = "sensor-smoke,sensor-co,sensor-temperature,bridge,detector,sprinkler-actuator,door-actuator,responder"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "dashboard")

	publishMS, err := strconv.Atoi(orDefault(os.Getenv("PUBLISH_MS"), "1000"))
	if err != nil || publishMS <= 0 {
		log.Error("PUBLISH_MS must be a positive integer")
		os.Exit(2)
	}
	ttl, err := strconv.Atoi(orDefault(os.Getenv("COMPONENT_TTL"), "10"))
	if err != nil || ttl <= 0 {
		log.Error("COMPONENT_TTL must be a positive integer")
		os.Exit(2)
	}
	state := dashboard.New(dashboard.Config{
		ComponentTTL: time.Duration(ttl) * time.Second,
		Expected:     splitList(orDefault(os.Getenv("EXPECTED"), defaultExpected)),
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	h := health.New("dashboard")
	mux := http.NewServeMux()
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	srv := &http.Server{Addr: orDefault(os.Getenv("LISTEN"), ":8095"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	mqttURL := orDefault(os.Getenv("MQTT_URL"), "tcp://127.0.0.1:1883")
	conn, err := mqttx.Connect(ctx, mqttURL, "dashboard")
	if err != nil {
		log.Error("mqtt connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())
	go mqttx.PublishHeartbeats(ctx, conn, "dashboard", 2*time.Second, nil)

	sub := func(filter string, fn func(payload []byte)) {
		if err := conn.Subscribe(ctx, filter, 1, func(_ string, payload []byte) { fn(payload) }); err != nil {
			log.Error("subscribe", "filter", filter, "err", err)
			os.Exit(1)
		}
	}
	sub("decisions/#", func(p []byte) {
		var d proto.Decision
		if json.Unmarshal(p, &d) == nil {
			state.ApplyDecision(d)
		}
	})
	sub("commands/sprinkler/#", func(p []byte) {
		var c proto.Command
		if json.Unmarshal(p, &c) == nil {
			state.ApplyCommand(c)
		}
	})
	sub("route/#", func(p []byte) {
		var r proto.RouteMsg
		if json.Unmarshal(p, &r) == nil {
			state.ApplyRoute(r)
		}
	})
	sub("heartbeat/#", func(p []byte) {
		var hb proto.Heartbeat
		if json.Unmarshal(p, &hb) == nil {
			state.ApplyHeartbeat(hb)
		}
	})

	bs := buildsim.New(orDefault(os.Getenv("BUILDSIM_URL"), "http://127.0.0.1:9090"), 2*time.Second)
	h.SetReady(true)
	log.Info("started", "mqtt", mqttURL)

	tick := time.NewTicker(time.Duration(publishMS) * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			log.Info("stopped")
			return
		case <-tick.C:
		}
		for name, put := range map[string]func() error{
			"room layers": func() error { return bs.PutRoomLayers(ctx, state.Layers()) },
			"effects":     func() error { return bs.PutEffects(ctx, state.Effects()) },
			"alerts":      func() error { return bs.PutAlerts(ctx, state.Alerts()) },
		} {
			if err := put(); err != nil {
				log.Warn("overlay not updated", "what", name, "err", err)
			}
		}
		h.Beat()
	}
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
