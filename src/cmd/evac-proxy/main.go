// evac-proxy sits between the course occupancysim and BuildSim and makes
// people in a burning room walk to the nearest safe room (see internal/evac).
// occupancysim is pointed at this proxy instead of at BuildSim; it is not
// modified.
//
// Configuration (environment):
//
//	UPSTREAM      BuildSim base URL                    (default http://127.0.0.1:9090)
//	DETECTOR_URL  detector base URL (GET /status)      (default http://127.0.0.1:8093)
//	SAFE_ROOMS    comma list of level/room to go to    (required)
//	SIM_SPEED     sim seconds per wall second          (default 10)
//	WALK_UNITS    plan units walked per sim second     (default 2.6: 1.3 m/s at 0.5 m/unit)
//	REACTION      sim seconds before people start      (default 10)
//	SPREAD        plan units of side-to-side spread    (default 3)
//	LISTEN        address                              (default :8098)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/evac"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "evac-proxy")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	upstream := env("UPSTREAM", "http://127.0.0.1:9090")
	detector := env("DETECTOR_URL", "http://127.0.0.1:8093")
	speed := envFloat("SIM_SPEED", 10)
	safe := strings.Split(os.Getenv("SAFE_ROOMS"), ",")
	if os.Getenv("SAFE_ROOMS") == "" {
		log.Error("SAFE_ROOMS is required, for example SAFE_ROOMS=level0/A1016")
		os.Exit(2)
	}

	bs := buildsim.New(upstream, 5*time.Second)
	route := func(level, room string, burning map[string]bool) (evac.Path, string, error) {
		var cands []evac.Candidate
		for _, s := range safe {
			sl, sn, _ := strings.Cut(strings.TrimSpace(s), "/")
			if sl != level { // the walk stays on one floor
				continue
			}
			r, err := bs.Route(ctx, level, room, sl, sn)
			if err != nil {
				continue
			}
			c := evac.Candidate{Dest: sl + "/" + sn, Path: make(evac.Path, len(r.Path))}
			for i, pt := range r.Path {
				c.Path[i] = evac.Point{X: pt.X, Y: pt.Y}
				c.Rooms = append(c.Rooms, pt.Name)
			}
			cands = append(cands, c)
		}
		best, ok := evac.PickRoute(cands, level, level+"/"+room, burning)
		if !ok {
			return nil, "", errors.New("no route to a safe room that avoids the fire")
		}
		return best.Path, best.Dest, nil
	}

	client := &http.Client{Timeout: 3 * time.Second}
	fire := func(ctx context.Context) (map[string]bool, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, detector+"/status", nil)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var rooms []struct{ Room, State string }
		if err := json.NewDecoder(resp.Body).Decode(&rooms); err != nil {
			return nil, err
		}
		out := map[string]bool{}
		for _, r := range rooms {
			if r.State == "fire" {
				out[r.Room] = true
			}
		}
		return out, nil
	}

	started := time.Now()
	now := func() float64 { return time.Since(started).Seconds() * speed }
	eng := evac.New(evac.Config{
		Speed: envFloat("WALK_UNITS", 2.6), Reaction: envFloat("REACTION", 10), Spread: envFloat("SPREAD", 3),
		ReactionSpread: envFloat("REACTION_SPREAD", 60), SpeedVariation: envFloat("SPEED_VARIATION", 0.25),
	}, route)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/", evac.NewProxy(upstream, eng, fire, now))
	srv := &http.Server{Addr: env("LISTEN", ":8098"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()
	log.Info("started", "listen", srv.Addr, "upstream", upstream, "safe", safe)
	<-ctx.Done()
	sc, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(sc)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return f
	}
	return def
}
