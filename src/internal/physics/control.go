package physics

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ParseGrowth maps a fire growth class to its t-squared coefficient.
func ParseGrowth(class string) (alpha float64, ok bool) {
	switch class {
	case "slow":
		return AlphaSlow, true
	case "medium":
		return AlphaMedium, true
	case "fast":
		return AlphaFast, true
	}
	return 0, false
}

// ActiveScenario describes one source currently acting on a room.
type ActiveScenario struct {
	Kind      string  `json:"kind"` // fire, cooking, dust or drift
	Room      string  `json:"room"`
	StartedAt float64 `json:"started_at"` // sim seconds
	Remaining float64 `json:"remaining,omitempty"`
}

var kindNames = map[Kind]string{KindFire: "fire", KindCooking: "cooking", KindDust: "dust", KindDrift: "drift"}

// Ignite starts a fire of the given growth class in a room, at the current
// sim time. Several rooms can burn at once.
func (e *Engine) Ignite(room, growth string) error {
	alpha, ok := ParseGrowth(growth)
	if !ok {
		return fmt.Errorf("growth must be slow, medium or fast, not %q", growth)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.index[room]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownRoom, room)
	}
	e.scenarios = append(e.scenarios, Scenario{Kind: KindFire, Room: i, T0: e.simTime, Alpha: alpha})
	return nil
}

// StartBenign starts a false-alarm source (cooking or dust) in a room.
func (e *Engine) StartBenign(kind, room string, duration float64) error {
	var k Kind
	switch kind {
	case "cooking":
		k = KindCooking
	case "dust":
		k = KindDust
	default:
		return fmt.Errorf("benign kind must be cooking or dust, not %q", kind)
	}
	if duration <= 0 {
		return errors.New("duration must be positive")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.index[room]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownRoom, room)
	}
	e.scenarios = append(e.scenarios, Scenario{Kind: k, Room: i, T0: e.simTime, Duration: duration})
	return nil
}

// Clear stops every source. Smoke and heat already in a room decay on their
// own; they do not vanish.
func (e *Engine) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scenarios = nil
}

// Reset returns every room to ambient and stops every source, as if the fire
// never happened. Sim time is not touched, so every consumer's clock keeps
// running. Used between repeated measurement runs.
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scenarios = nil
	for i := range e.rooms {
		e.rooms[i] = Room{Volume: e.rooms[i].Volume, T: e.cfg.Params.TOut}
		e.sprAt[i] = -1
	}
}

// Active lists the sources still acting at the current sim time.
func (e *Engine) Active() []ActiveScenario {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []ActiveScenario{}
	for _, sc := range e.scenarios {
		a := ActiveScenario{Kind: kindNames[sc.Kind], Room: e.cfg.Rooms[sc.Room].Name, StartedAt: sc.T0}
		if sc.Kind != KindFire && sc.Kind != KindDrift {
			end := sc.T0 + sc.Duration
			if e.simTime >= end {
				continue // a finished cooking or dust source
			}
			a.Remaining = end - e.simTime
		}
		out = append(out, a)
	}
	return out
}

// RoomNames lists the rooms that have physics, in configuration order.
func (e *Engine) RoomNames() []string {
	out := make([]string, 0, len(e.cfg.Rooms))
	for _, r := range e.cfg.Rooms {
		out = append(out, r.Name)
	}
	return out
}

// ErrUnknownRoom is returned for a room the engine does not simulate.
var ErrUnknownRoom = errors.New("physics: unknown room")

// ControlHandler is the machine API for scenarios, used by the scenario
// console (a separate process):
//
//	POST /control/fire    {"room","growth"}
//	POST /control/benign  {"kind","room","duration"}
//	POST /control/clear
//	POST /control/reset   (rooms back to ambient, as if nothing happened)
//	GET  /control/state
func (e *Engine) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, err error) {
		code := http.StatusBadRequest
		if errors.Is(err, ErrUnknownRoom) {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
	}
	mux.HandleFunc("POST /control/fire", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Room, Growth string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := e.Ignite(req.Room, req.Growth); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /control/benign", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Kind, Room string
			Duration   float64
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := e.StartBenign(req.Kind, req.Room, req.Duration); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /control/clear", func(w http.ResponseWriter, _ *http.Request) {
		e.Clear()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /control/reset", func(w http.ResponseWriter, _ *http.Request) {
		e.Reset()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /control/state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		state := map[string]any{"sim_time": e.Time(), "rooms": e.RoomNames(), "active": e.Active()}
		e.mu.Lock()
		clock := e.clock
		e.mu.Unlock()
		if clock != nil {
			state["clock_drift"] = clock()
		}
		_ = json.NewEncoder(w).Encode(state)
	})
	return mux
}
