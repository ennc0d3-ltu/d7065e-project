package physics

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SprinklerSource reads the state an actuator has reached. In production it
// is the BuildSim client; in tests, a fake.
type SprinklerSource interface {
	GetActuatorState(ctx context.Context, id string) (string, error)
}

// PollSprinklers reads every room's sprinkler state into the engine and
// returns the number of failed reads. A failed read keeps the last known
// state: an unreachable BuildSim must not look like "sprinkler switched off".
func (e *Engine) PollSprinklers(ctx context.Context, src SprinklerSource) (errs int) {
	for _, rs := range e.cfg.Rooms {
		if rs.SprinklerID == "" {
			continue
		}
		state, err := src.GetActuatorState(ctx, rs.SprinklerID)
		if err != nil {
			errs++
			continue
		}
		e.SetSprinkler(rs.Name, parseSprinkler(state))
	}
	return errs
}

// parseSprinkler maps an actuator state to [0,1]: "on" 1, "off" 0, or a
// number clamped to the range. Anything unrecognised counts as off, so the
// physics never assumes suppression it has not been told about.
func parseSprinkler(state string) float64 {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "on":
		return 1
	case "off":
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(state), 64)
	if err != nil {
		return 0
	}
	return min(1, max(0, v))
}

// Run polls actuator state and ticks the world every `every` of wall time,
// advancing simDt sim seconds per tick, until ctx is cancelled.
func (e *Engine) Run(ctx context.Context, src SprinklerSource, every time.Duration, simDt float64) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.PollSprinklers(ctx, src)
			e.Tick(simDt)
		}
	}
}

// truthJSON is the wire form of one room's ground truth.
type truthJSON struct {
	Room    string  `json:"room"`
	TempC   float64 `json:"temp_c"`
	Smoke   float64 `json:"smoke"`
	COppm   float64 `json:"co_ppm"`
	SimTime float64 `json:"sim_time"`
}

// TruthHandler serves GET /truth/rooms/{level}/{room}. Only the sensor
// gateway should call it; it is deliberately not exposed through BuildSim.
func (e *Engine) TruthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /truth/rooms/{level}/{room}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("level") + "/" + r.PathValue("room")
		e.mu.Lock()
		i, ok := e.index[name]
		var rm Room
		var now float64
		if ok {
			rm, now = e.rooms[i], e.simTime
		}
		e.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(truthJSON{
			Room: name, TempC: rm.T, Smoke: rm.K, COppm: rm.C, SimTime: now,
		})
	})
	return mux
}
