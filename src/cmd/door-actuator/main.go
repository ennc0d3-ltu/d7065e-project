// door-actuator operates the rooms' egress door locks. The doors are locked by
// default; the detector's door commands unlock a room's door while the room is
// in alarm, which also puts the door back into BuildSim's evacuation routes.
// It is the only writer of BuildSim's door collection (a PUT replaces it all).
//
// It fails safe, following fire-safety practice for access-controlled egress
// doors (D19): every door is unlocked at start-up, at shutdown, and whenever the
// detector has been silent for 30 sim seconds; the doors lock again only after
// the detector has been heard and quiet.
//
// Configuration (environment):
//
//	DOORS         comma list of level/room:entry-node (required)
//	MQTT_URL      broker URL                          (default tcp://127.0.0.1:1883)
//	BUILDSIM_URL  BuildSim base URL                   (default http://127.0.0.1:9090)
//	SIM_SPEED     sim seconds per wall second         (default 10)
//	QUIET_FOR     sim seconds of quiet before locking (default 20)
//	LISTEN        health endpoint                     (default :8102)
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/doors"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/health"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "door-actuator")

	list, err := doors.ParseDoors(os.Getenv("DOORS"))
	if err != nil {
		log.Error("DOORS", "err", err, "example", "DOORS=level0/A1008:470,level0/A1024B:512")
		os.Exit(2)
	}
	speed := envFloat("SIM_SPEED", 10)
	if speed <= 0 {
		log.Error("SIM_SPEED must be positive")
		os.Exit(2)
	}
	cfg := doors.Config{
		Doors:    list,
		Silence:  time.Duration(30 / speed * float64(time.Second)), // the actuator's 30 sim s limit, in wall time
		QuietFor: envFloat("QUIET_FOR", 20),
	}
	ctrl := doors.New(cfg, time.Now)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	h := health.New("door-actuator")
	mux := http.NewServeMux()
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	srv := &http.Server{Addr: orDefault(os.Getenv("LISTEN"), ":8102"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	conn, err := mqttx.Connect(ctx, orDefault(os.Getenv("MQTT_URL"), "tcp://127.0.0.1:1883"), "door-actuator")
	if err != nil {
		log.Error("mqtt connect", "err", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())
	go mqttx.PublishHeartbeats(ctx, conn, "door-actuator", 2*time.Second, nil)
	if err := conn.Subscribe(ctx, "commands/door/#", 1, func(topic string, payload []byte) {
		var cmd proto.DoorCommand
		if err := json.Unmarshal(payload, &cmd); err != nil {
			log.Warn("door command rejected", "topic", topic, "err", err)
			return
		}
		if err := ctrl.OnCommand(cmd); err != nil {
			log.Warn("door command rejected", "topic", topic, "err", err)
		}
	}); err != nil {
		log.Error("subscribe", "err", err)
		os.Exit(1)
	}
	if err := conn.Subscribe(ctx, proto.HeartbeatTopic("detector"), 1, func(_ string, payload []byte) {
		var hb proto.Heartbeat
		if json.Unmarshal(payload, &hb) == nil {
			ctrl.OnHeartbeat(hb)
		}
	}); err != nil {
		log.Error("subscribe", "err", err)
		os.Exit(1)
	}

	bs := buildsim.New(orDefault(os.Getenv("BUILDSIM_URL"), "http://127.0.0.1:9090"), 2*time.Second)
	var last []buildsim.Door
	var lastPut time.Time
	write := func(c context.Context) {
		want := doors.BuildSimDoors(ctrl.Doors(), ctrl.Desired())
		// BuildSim keeps everything in memory, so write again every 3 s: a
		// BuildSim restart is then repaired, and the doors stay unlocked meanwhile
		if reflect.DeepEqual(want, last) && time.Since(lastPut) < 3*time.Second {
			return
		}
		if err := bs.PutDoors(c, want); err != nil {
			log.Warn("BuildSim door write failed", "err", err)
			return
		}
		for i, d := range want {
			if i >= len(last) || last[i].LockState != d.LockState {
				log.Info("door", "room", d.Room, "level", d.Level, "lock", d.LockState)
			}
		}
		last, lastPut = want, time.Now()
		h.SetReady(true)
	}
	log.Info("started", "doors", len(list), "silence", cfg.Silence.String())

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			// a fail-safe lock releases when its power goes: so release at shutdown
			ctrl.ReleaseAll()
			sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			write(sc)
			cancel()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			log.Info("stopped")
			return
		case <-tick.C:
			write(ctx)
			h.Beat()
		}
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func envFloat(k string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return f
	}
	return def
}
