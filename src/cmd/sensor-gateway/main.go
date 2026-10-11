// sensor-gateway samples ground truth from physics-sim, degrades it the way
// real sensors would (noise, lag, injected faults) and publishes readings to
// MQTT. It also registers its sensors in BuildSim on every start (NFR4).
//
// Configuration (environment):
//
//	TRUTH_URL    physics-sim base URL    (default http://127.0.0.1:8090)
//	MQTT_URL     broker URL              (default tcp://127.0.0.1:1883)
//	BUILDSIM_URL BuildSim base URL       (default http://127.0.0.1:9090)
//	ROOMS        comma list of level/room (required)
//	SAMPLE_MS    sampling period in ms   (default 1000)
//	LISTEN       health + fault endpoint (default :8091)
//	ACTUATOR_URL actuator base URL for the direct, broker-free path (optional)
//	SEED         base random seed        (default 1)
//	SENSOR_TYPES comma list of kinds this process runs: smoke, co, temperature
//	             (default: all three). Run the same image once per kind to get
//	             one process per sensor type.
//	PROCESS      name for heartbeats, MQTT client id and outbox
//	             (default sensor-gateway, or sensor-<kind> for a single kind)
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
)

func main() {
	kinds := splitList(os.Getenv("SENSOR_TYPES"))
	name := os.Getenv("PROCESS")
	if name == "" {
		name = "sensor-gateway"
		if len(kinds) == 1 {
			name = "sensor-" + kinds[0]
		}
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", name)

	rooms := splitList(os.Getenv("ROOMS"))
	if len(rooms) == 0 {
		log.Error("ROOMS is required, for example ROOMS=level0/A109,level0/A108")
		os.Exit(2)
	}
	sampleMS, err := strconv.Atoi(orDefault(os.Getenv("SAMPLE_MS"), "1000"))
	if err != nil || sampleMS <= 0 {
		log.Error("SAMPLE_MS must be a positive integer")
		os.Exit(2)
	}
	seed, _ := strconv.ParseInt(orDefault(os.Getenv("SEED"), "1"), 10, 64)

	var specs []gateway.SensorSpec
	for i, r := range rooms {
		specs = append(specs, gateway.RoomSensors(r, seed+int64(i))...)
	}

	specs, err = gateway.FilterKinds(specs, kinds)
	if err != nil {
		log.Error("SENSOR_TYPES", "err", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	h := health.New(name)

	// Liveness is served from the start; the fault endpoint answers 503 until
	// the gateway exists (after registration and the MQTT connect).
	var faults atomic.Pointer[http.Handler]
	mux := http.NewServeMux()
	mux.HandleFunc("/faults/", func(w http.ResponseWriter, r *http.Request) {
		if fh := faults.Load(); fh != nil {
			(*fh).ServeHTTP(w, r)
			return
		}
		http.Error(w, "starting", http.StatusServiceUnavailable)
	})
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	srv := &http.Server{Addr: orDefault(os.Getenv("LISTEN"), ":8091"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	// Register in BuildSim and keep registering: BuildSim keeps everything in
	// memory, so a restart there would otherwise leave every reading unaccepted.
	bs := buildsim.New(orDefault(os.Getenv("BUILDSIM_URL"), "http://127.0.0.1:9090"), 2*time.Second)
	var lastOK bool
	go buildsim.KeepRegistered(ctx, bs, gateway.Equipment(specs), 3*time.Second, func(res buildsim.BulkResult, err error) {
		switch {
		case err != nil && lastOK:
			log.Warn("BuildSim unreachable: will keep trying to register", "err", err)
			lastOK = false
		case err == nil && res.Created > 0:
			log.Info("registered sensors", "created", res.Created, "skipped", res.Skipped)
			lastOK = true
		case err == nil:
			lastOK = true
		}
	})

	mqttURL := orDefault(os.Getenv("MQTT_URL"), "tcp://127.0.0.1:1883")
	conn, err := mqttx.Connect(ctx, mqttURL, name)
	if err != nil {
		log.Error("mqtt connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())
	go mqttx.PublishHeartbeats(ctx, conn, name, 2*time.Second, nil)

	truth := gateway.NewTruthClient(orDefault(os.Getenv("TRUTH_URL"), "http://127.0.0.1:8090"))
	q, err := mqttx.StartQueueing(ctx, conn, name)
	if err != nil {
		log.Error("outbox", "err", err)
		os.Exit(1)
	}
	if n := q.Outbox().Len(); n > 0 {
		log.Info("replaying messages left from the previous run", "count", n)
	}
	var sink gateway.Sink = mqttx.NewReadingSink(q, 1)
	if url := os.Getenv("ACTUATOR_URL"); url != "" {
		// a second path that does not depend on the broker (D6)
		sink = gateway.NewMultiSink(sink, gateway.NewHTTPSink(url))
	}
	gw := gateway.New(specs, truth, sink)
	fh := gw.FaultHandler()
	faults.Store(&fh)
	h.SetReady(true)
	log.Info("started", "rooms", len(rooms), "sensors", len(specs), "mqtt", mqttURL)

	tick := time.NewTicker(time.Duration(sampleMS) * time.Millisecond)
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
			pub, errs := gw.Sample(ctx)
			h.Beat()
			if errs > 0 {
				log.Warn("sample had errors", "published", pub, "errors", errs)
			}
		}
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
