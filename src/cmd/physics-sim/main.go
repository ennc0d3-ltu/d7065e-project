// physics-sim steps the ground-truth fire model and serves it to the sensor
// gateway. It reads sprinkler actuator state back from BuildSim, which closes
// the control loop (docs/L1/04-physical-model.md §7).
//
// Configuration (environment):
//
//	BUILDSIM_URL   BuildSim base URL            (default http://127.0.0.1:9090)
//	LISTEN         address for /truth and /healthz (default :8090)
//	ROOMS          comma list level/room:volume_m3:sprinkler_id
//	DOORS          comma list roomA|roomB|openness
//	FIRE           optional level/room:growth(slow|medium|fast):start_s
//	BENIGN         optional comma list kind(cooking|dust):level/room:start_s:duration_s
//	TICK_MS        wall-clock tick in ms         (default 1000)
//	SIM_DT         sim seconds per tick          (default 1)
//	OCCUPANCY_URL  course occupancysim base URL; if set, its clock is aligned to ours (optional)
//	OCC_START      time of day the run starts at (default 10:30)
//	CLOCK_DRIFT_WARN  sim seconds of drift between the two clocks that raise a warning (default 10)
package main

import (
	"context"
	"fmt"
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
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/physics"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("proc", "physics-sim")
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Error("bad configuration", "err", err)
		os.Exit(2)
	}

	engine := physics.NewEngine(cfg.engine)
	bs := buildsim.New(cfg.buildsimURL, 2*time.Second)
	h := health.New("physics-sim")

	mux := http.NewServeMux()
	mux.Handle("/truth/", engine.TruthHandler())
	mux.Handle("/control/", engine.ControlHandler()) // used by the scenario console
	mux.Handle("/healthz", h.Handler())
	mux.Handle("/readyz", h.Handler())
	srv := &http.Server{Addr: cfg.listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	// People: body heat follows BuildSim's occupancy, which occupancysim writes.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			engine.PollPeople(ctx, func(ctx context.Context) (map[string]int, error) {
				occ, err := bs.GetOccupancy(ctx)
				if err != nil {
					return nil, err
				}
				counts := make(map[string]int, len(occ))
				for room, o := range occ {
					counts[room] = len(o.Persons)
				}
				return counts, nil
			})
		}
	}()
	// Clock: run occupancysim at our speed from the same start time (D5).
	if occURL := os.Getenv("OCCUPANCY_URL"); occURL != "" {
		factor := cfg.simDt / cfg.tick.Seconds()
		start := orDefault(os.Getenv("OCC_START"), "10:30")
		go func() {
			for attempt := 1; ; attempt++ {
				err := physics.AlignOccupancyClock(ctx, occURL, factor, start)
				if err == nil {
					log.Info("occupancy clock aligned", "factor", factor, "start", start)
					// the two clocks are aligned, not locked: watch them drift apart
					mon := physics.NewDriftMonitor(occURL, factor)
					mon.Sim = engine.Time
					mon.Warn, _ = strconv.ParseFloat(orDefault(os.Getenv("CLOCK_DRIFT_WARN"), "10"), 64)
					engine.SetClockStatus(mon.Status)
					mon.Run(ctx, 30*time.Second, func(st physics.ClockStatus) {
						if st.Exceeded {
							log.Warn("occupancy clock has drifted from the physics clock", "drift_s", st.DriftSec, "warn_s", mon.Warn)
						} else {
							log.Info("occupancy clock drift back within bounds", "drift_s", st.DriftSec)
						}
					})
					return
				}
				log.Warn("occupancy clock not aligned yet, retrying", "attempt", attempt, "err", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(min(attempt, 10)) * time.Second):
				}
			}
		}()
	}
	h.SetReady(true)
	log.Info("started", "listen", cfg.listen, "rooms", len(cfg.engine.Rooms), "buildsim", cfg.buildsimURL)
	engine.Run(ctx, bs, cfg.tick, cfg.simDt)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Info("stopped", "sim_time", engine.Time())
}

type config struct {
	buildsimURL string
	listen      string
	tick        time.Duration
	simDt       float64
	engine      physics.Config
}

func loadConfig(get func(string) string) (config, error) {
	c := config{
		buildsimURL: orDefault(get("BUILDSIM_URL"), "http://127.0.0.1:9090"),
		listen:      orDefault(get("LISTEN"), ":8090"),
	}
	tickMS, err := strconv.Atoi(orDefault(get("TICK_MS"), "1000"))
	if err != nil || tickMS <= 0 {
		return c, fmt.Errorf("TICK_MS must be a positive integer")
	}
	c.tick = time.Duration(tickMS) * time.Millisecond
	if c.simDt, err = strconv.ParseFloat(orDefault(get("SIM_DT"), "1"), 64); err != nil || c.simDt <= 0 {
		return c, fmt.Errorf("SIM_DT must be a positive number")
	}

	c.engine = physics.Config{
		Params: physics.DefaultParams(), Yields: physics.DefaultYields(), Sprinkler: physics.DefaultSprinkler(),
	}
	index := map[string]int{}
	for _, spec := range split(get("ROOMS")) {
		f := strings.Split(spec, ":")
		if len(f) != 3 {
			return c, fmt.Errorf("ROOMS entry %q: want level/room:volume:sprinkler_id", spec)
		}
		vol, err := strconv.ParseFloat(f[1], 64)
		if err != nil || vol <= 0 {
			return c, fmt.Errorf("ROOMS entry %q: bad volume", spec)
		}
		index[f[0]] = len(c.engine.Rooms)
		c.engine.Rooms = append(c.engine.Rooms, physics.RoomSpec{Name: f[0], Volume: vol, SprinklerID: f[2]})
	}
	if len(c.engine.Rooms) == 0 {
		return c, fmt.Errorf("ROOMS is empty")
	}
	for _, spec := range split(get("DOORS")) {
		f := strings.Split(spec, "|")
		if len(f) != 3 {
			return c, fmt.Errorf("DOORS entry %q: want roomA|roomB|openness", spec)
		}
		open, err := strconv.ParseFloat(f[2], 64)
		if err != nil || open < 0 || open > 1 {
			return c, fmt.Errorf("DOORS entry %q: openness must be in [0,1]", spec)
		}
		if _, ok := index[f[0]]; !ok {
			return c, fmt.Errorf("DOORS entry %q: unknown room %q", spec, f[0])
		}
		if _, ok := index[f[1]]; !ok {
			return c, fmt.Errorf("DOORS entry %q: unknown room %q", spec, f[1])
		}
		c.engine.Doors = append(c.engine.Doors, physics.NamedDoor{A: f[0], B: f[1], Openness: open})
	}
	if spec := get("FIRE"); spec != "" {
		f := strings.Split(spec, ":")
		if len(f) != 3 {
			return c, fmt.Errorf("FIRE %q: want level/room:growth:start_s", spec)
		}
		room, ok := index[f[0]]
		if !ok {
			return c, fmt.Errorf("FIRE %q: unknown room", spec)
		}
		alpha, ok := map[string]float64{"slow": physics.AlphaSlow, "medium": physics.AlphaMedium, "fast": physics.AlphaFast}[f[1]]
		if !ok {
			return c, fmt.Errorf("FIRE %q: growth must be slow, medium or fast", spec)
		}
		t0, err := strconv.ParseFloat(f[2], 64)
		if err != nil {
			return c, fmt.Errorf("FIRE %q: bad start", spec)
		}
		c.engine.Scenarios = append(c.engine.Scenarios, physics.Scenario{
			Kind: physics.KindFire, Room: room, T0: t0, Alpha: alpha,
		})
	}
	for _, spec := range split(get("BENIGN")) {
		sc, err := physics.ParseBenign(spec, index)
		if err != nil {
			return c, err
		}
		c.engine.Scenarios = append(c.engine.Scenarios, sc)
	}
	return c, nil
}

func split(s string) []string {
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
