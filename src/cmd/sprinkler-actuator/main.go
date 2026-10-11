// sprinkler-actuator drives each room's sprinkler in BuildSim from the
// detector's commands, and takes over with local rules if the detector goes
// silent (decision D6). It registers its actuators on every start (NFR4) and
// adopts the state BuildSim already holds, so a restart mid-fire cannot switch
// a sprinkler off.
//
// Configuration (environment):
//
//	MQTT_URL     broker URL                  (default tcp://127.0.0.1:1883)
//	BUILDSIM_URL BuildSim base URL           (default http://127.0.0.1:9090)
//	ROOMS        comma list of level/room    (required)
//	LISTEN       health endpoint             (default :8094)
//	PUT_MS       re-assert interval in ms    (default 1000)
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
	"sync"
	"syscall"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/actuator"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "sprinkler-actuator")

	rooms := splitList(os.Getenv("ROOMS"))
	if len(rooms) == 0 {
		log.Error("ROOMS is required, for example ROOMS=level0/A109,level0/A108")
		os.Exit(2)
	}
	putMS, err := strconv.Atoi(orDefault(os.Getenv("PUT_MS"), "1000"))
	if err != nil || putMS <= 0 {
		log.Error("PUT_MS must be a positive integer")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	h := health.New("sprinkler-actuator")

	mux := http.NewServeMux()
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	var onReading func(gateway.Reading)
	mux.Handle("/readings", actuator.ReadingsHandler(func(r gateway.Reading) {
		if f := onReading; f != nil {
			f(r)
		}
	}))
	srv := &http.Server{Addr: orDefault(os.Getenv("LISTEN"), ":8094"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	// Register in BuildSim, retrying: it may still be starting (NFR4).
	bs := buildsim.New(orDefault(os.Getenv("BUILDSIM_URL"), "http://127.0.0.1:9090"), 2*time.Second)
	var eq []buildsim.Equipment
	for _, r := range rooms {
		eq = append(eq, actuator.Equipment(r))
	}
	// First registration is synchronous so the restored state below can be read.
	for attempt := 1; ; attempt++ {
		res, err := bs.BulkCreate(ctx, eq)
		if err == nil {
			log.Info("registered sprinklers", "created", res.Created, "skipped", res.Skipped)
			break
		}
		log.Warn("register failed, retrying", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(min(attempt, 10)) * time.Second):
		}
	}
	// ...and then repeated, so a BuildSim restart is repaired.
	go buildsim.KeepRegistered(ctx, bs, eq, 3*time.Second, nil)

	// One controller per room. Restore adopts the state BuildSim already has.
	var mu sync.Mutex
	ctls := map[string]*actuator.Controller{}
	for _, r := range rooms {
		c := actuator.New(r, actuator.DefaultConfig())
		if state, err := bs.GetActuatorState(ctx, actuator.SprinklerID(r)); err == nil && state == "on" {
			c.Restore(true)
			log.Warn("sprinkler was already on at start: keeping it on", "room", r)
		}
		ctls[r] = c
	}
	var simNow float64

	mqttURL := orDefault(os.Getenv("MQTT_URL"), "tcp://127.0.0.1:1883")
	conn, err := mqttx.Connect(ctx, mqttURL, "sprinkler-actuator")
	if err != nil {
		log.Error("mqtt connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())
	q, err := mqttx.StartQueueing(ctx, conn, "sprinkler-actuator")
	if err != nil {
		log.Error("outbox", "err", err)
		os.Exit(1)
	}
	if n := q.Outbox().Len(); n > 0 {
		log.Info("replaying messages left from the previous run", "count", n)
	}
	go mqttx.PublishHeartbeats(ctx, conn, "sprinkler-actuator", 2*time.Second, func() float64 {
		mu.Lock()
		defer mu.Unlock()
		return simNow
	})

	subscribe := func(filter string, fn func(topic string, payload []byte)) {
		if err := conn.Subscribe(ctx, filter, 1, fn); err != nil {
			log.Error("subscribe", "filter", filter, "err", err)
			os.Exit(1)
		}
	}
	subscribe("commands/sprinkler/#", func(topic string, payload []byte) {
		var cmd proto.Command
		if err := json.Unmarshal(payload, &cmd); err != nil {
			log.Warn("bad command", "topic", topic, "err", err)
			q.DeadLetter(ctx, topic, payload, err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if c, ok := ctls[cmd.Room]; ok {
			simNow = max(simNow, cmd.SimTime)
			if !c.OnCommand(cmd) {
				log.Debug("command not applied", "room", cmd.Room, "seq", cmd.Seq)
			}
		}
	})
	subscribe(proto.HeartbeatTopic("detector"), func(_ string, payload []byte) {
		var hb proto.Heartbeat
		if json.Unmarshal(payload, &hb) != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		simNow = max(simNow, hb.SimTime)
		for _, c := range ctls {
			c.OnHeartbeat(hb)
		}
	})
	// Readings arrive over MQTT and, independently of the broker, straight
	// from the gateway over HTTP (D6).
	apply := func(r gateway.Reading) {
		mu.Lock()
		defer mu.Unlock()
		simNow = max(simNow, r.SimTime)
		if c, ok := ctls[r.Room]; ok {
			c.OnReading(r)
		}
	}
	onReading = apply
	subscribe("sensors/#", func(_ string, payload []byte) {
		var r gateway.Reading
		if json.Unmarshal(payload, &r) == nil {
			apply(r)
		}
	})

	h.SetReady(true)
	log.Info("started", "rooms", len(rooms), "mqtt", mqttURL)

	written := map[string]string{}
	lastPut := map[string]time.Time{}
	tick := time.NewTicker(100 * time.Millisecond)
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
		mu.Lock()
		outs := map[string]actuator.Output{}
		for r, c := range ctls {
			outs[r] = c.Step(simNow)
		}
		mu.Unlock()
		h.Beat()

		for r, out := range outs {
			want := actuator.StateString(out.On)
			changed := written[r] != want
			if !changed && time.Since(lastPut[r]) < time.Duration(putMS)*time.Millisecond {
				continue
			}
			if err := bs.PutActuatorState(ctx, actuator.SprinklerID(r), want); err != nil {
				log.Warn("write to BuildSim failed", "room", r, "err", err)
				continue
			}
			if changed {
				log.Info("sprinkler state", "room", r, "state", want, "source", out.Source.String(), "reason", out.Reason)
			}
			written[r], lastPut[r] = want, time.Now()
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
