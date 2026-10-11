package physics

import (
	"fmt"
	"sync"
)

// RoomSpec names a room and gives its air volume [m³].
type RoomSpec struct {
	Name        string // "<level>/<room>", the BuildSim occupancy key form
	Volume      float64
	SprinklerID string // BuildSim actuator id of the room's sprinkler; "" if none
}

// NamedDoor links two rooms by name.
type NamedDoor struct {
	A, B     string
	Openness float64
}

// Config describes the simulated world.
type Config struct {
	Params    Params
	Yields    Yields
	Sprinkler Sprinkler
	Rooms     []RoomSpec
	Doors     []NamedDoor
	Scenarios []Scenario // Scenario.Room indexes Rooms
}

// Engine is the ground-truth simulator. It is safe for concurrent use:
// one goroutine ticks while others read truth or report actuator state.
type Engine struct {
	mu     sync.Mutex
	cfg    Config
	index  map[string]int
	doors  []Door
	rooms  []Room
	people []int
	sprOn  []float64 // sprinkler state reported by BuildSim, in [0,1]
	sprAt  []float64 // time the sprinkler last came on; -1 if never
	// scenarios start from the configuration and can be added to or cleared
	// while the engine runs (see control.go)
	scenarios []Scenario
	simTime   float64
	// clock reports the occupancy clock drift when a monitor is attached
	clock func() ClockStatus
}

// SetClockStatus attaches a source for the occupancy clock drift, shown in the
// control state.
func (e *Engine) SetClockStatus(f func() ClockStatus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock = f
}

// NewEngine builds an engine with every room at ambient. It panics on a
// door that names an unknown room: that is a startup configuration error.
func NewEngine(cfg Config) *Engine {
	e := &Engine{
		cfg:    cfg,
		index:  make(map[string]int, len(cfg.Rooms)),
		rooms:  make([]Room, len(cfg.Rooms)),
		people: make([]int, len(cfg.Rooms)),
		sprOn:  make([]float64, len(cfg.Rooms)),
		sprAt:  make([]float64, len(cfg.Rooms)),
	}
	e.scenarios = append([]Scenario(nil), cfg.Scenarios...)
	for i, rs := range cfg.Rooms {
		e.index[rs.Name] = i
		e.rooms[i] = Room{Volume: rs.Volume, T: cfg.Params.TOut}
		e.sprAt[i] = -1
	}
	for _, d := range cfg.Doors {
		a, okA := e.index[d.A]
		b, okB := e.index[d.B]
		if !okA || !okB {
			panic(fmt.Sprintf("physics: door %q-%q names an unknown room", d.A, d.B))
		}
		e.doors = append(e.doors, Door{A: a, B: b, Openness: d.Openness})
	}
	return e
}

// Time is the current sim time [s].
func (e *Engine) Time() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.simTime
}

// Truth returns the ground-truth state of a room. It is never published to
// BuildSim: only the sensor gateway may read it (truth layer, L2).
func (e *Engine) Truth(room string) (Room, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.index[room]
	if !ok {
		return Room{}, false
	}
	return e.rooms[i], true
}

// SetSprinkler records the state the sprinkler actuator has reached, as read
// back from BuildSim. Unknown rooms are ignored.
func (e *Engine) SetSprinkler(room string, state float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.index[room]
	if !ok {
		return
	}
	if state > 0 && e.sprOn[i] == 0 {
		e.sprAt[i] = e.simTime
	}
	e.sprOn[i] = state
}

// SetPeople records the head-count read from BuildSim occupancy.
func (e *Engine) SetPeople(room string, n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i, ok := e.index[room]; ok {
		e.people[i] = n
	}
}

// Tick advances the world by dt seconds.
func (e *Engine) Tick(dt float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	in := make([]Inputs, len(e.rooms))
	for i := range in {
		in[i].People = e.people[i]
		in[i].Sprinkler = e.sprOn[i]
	}
	for _, sc := range e.scenarios {
		src := sc.InputsAt(e.simTime, e.sprOn[sc.Room], e.sprAt[sc.Room], e.cfg.Sprinkler, e.cfg.Yields)
		in[sc.Room].Heat += src.Heat
		in[sc.Room].Smoke += src.Smoke
		in[sc.Room].CO += src.CO
	}
	e.rooms = Step(e.rooms, e.doors, e.cfg.Params, in, dt)
	e.simTime += dt
}
