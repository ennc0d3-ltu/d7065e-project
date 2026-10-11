// Package dashboard turns the system's decisions, commands and heartbeats into
// what BuildSim's 3D viewer shows: a fire-risk heatmap, fire, warning,
// sprinkler and water effects, alert cards, and which components are down
// (NFR11). BuildSim replaces a whole collection on every PUT, so the dashboard
// is the only writer of room layers, effects and alerts.
package dashboard

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// maxAlerts is the most alerts BuildSim accepts.
const maxAlerts = 100

// Config sets the clock and the liveness rules.
type Config struct {
	Now          func() time.Time // default time.Now
	ComponentTTL time.Duration    // a component with no heartbeat for this long is down
	Expected     []string         // process names that must send heartbeats
}

type roomView struct {
	state       string // idle, warning, fire, cancelled
	reason      string
	stale       []string
	sprinklerOn bool // from the last command, not from the decision
}

// ComponentStatus is one expected process's liveness.
type ComponentStatus struct {
	Name     string
	Up       bool
	LastSeen time.Time // zero if never
}

// State is the dashboard's picture of the system. Safe for concurrent use.
type State struct {
	cfg   Config
	start time.Time

	mu       sync.Mutex
	rooms    map[string]*roomView
	routes   map[string][]string // burning room -> rooms on its evacuation route
	lastSeen map[string]time.Time
}

// New returns an empty state. Components get one TTL of grace after start
// before they count as down.
func New(cfg Config) *State {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &State{cfg: cfg, start: cfg.Now(), rooms: map[string]*roomView{}, routes: map[string][]string{}, lastSeen: map[string]time.Time{}}
}

func (s *State) room(key string) *roomView {
	r, ok := s.rooms[key]
	if !ok {
		r = &roomView{state: "idle"}
		s.rooms[key] = r
	}
	return r
}

// ApplyDecision records a detector decision.
func (s *State) ApplyDecision(d proto.Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.room(d.Room)
	if d.Refresh && d.State == r.state {
		r.stale = d.Stale // a re-announcement: keep the reason the state was entered for
	} else {
		r.state, r.reason, r.stale = d.State, d.Reason, d.Stale
	}
	if d.State != "fire" {
		delete(s.routes, d.Room) // a room that stopped burning has no evacuation route
	}
}

// ApplyRoute records the responder's evacuation route out of a burning room,
// replacing any earlier route for that room.
func (s *State) ApplyRoute(r proto.RouteMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[r.From] = append([]string(nil), r.Rooms...)
}

// ApplyCommand records a sprinkler command: the effect follows the command,
// not the decision, so the picture shows what was actually ordered.
func (s *State) ApplyCommand(c proto.Command) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.room(c.Room).sprinklerOn = c.Action == proto.ActionOn
}

// ApplyHeartbeat records that a process is alive. Processes that are not
// expected are ignored.
func (s *State) ApplyHeartbeat(hb proto.Heartbeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range s.cfg.Expected {
		if name == hb.Process {
			s.lastSeen[name] = s.cfg.Now()
			return
		}
	}
}

// Components reports each expected process, sorted by name.
func (s *State) Components() []ComponentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	out := make([]ComponentStatus, 0, len(s.cfg.Expected))
	for _, name := range s.cfg.Expected {
		seen, ok := s.lastSeen[name]
		up := now.Sub(s.start) <= s.cfg.ComponentTTL // grace until first heartbeat
		if ok {
			up = now.Sub(seen) <= s.cfg.ComponentTTL
		}
		out = append(out, ComponentStatus{Name: name, Up: up, LastSeen: seen})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *State) sortedRooms() []string {
	keys := make([]string, 0, len(s.rooms))
	for k := range s.rooms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func slug(room string) string { return strings.ReplaceAll(room, "/", "-") }

func split(room string) (level, name string) {
	level, name, _ = strings.Cut(room, "/")
	return level, name
}

// Layers returns the fire-risk heatmap: 0 idle, 1 warning or armed, 2 fire.
func (s *State) Layers() []buildsim.Layer {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := map[string]float64{}
	for k, r := range s.rooms {
		switch r.state {
		case "fire":
			values[k] = 2
		case "warning", "cancelled":
			values[k] = 1
		default:
			values[k] = 0
		}
	}
	layers := []buildsim.Layer{{
		ID: "fire-risk", Label: "Fire risk (0 idle, 1 armed, 2 fire)", Unit: "tier",
		Source: "decision-service estimate", Minimum: 0, Maximum: 2, Opacity: 0.7,
		Palette: []string{"#22c55e", "#facc15", "#dc2626"}, Values: values,
	}}
	// the safe path out: every room on a route except the burning room it starts in
	path := map[string]float64{}
	for from, rooms := range s.routes {
		for _, r := range rooms {
			if r != from {
				path[r] = 1
			}
		}
	}
	if len(path) > 0 {
		layers = append(layers, buildsim.Layer{
			ID: "evacuation-route", Label: "Evacuation route", Unit: "route",
			Source: "decision-service estimate", Minimum: 0, Maximum: 1, Opacity: 0.6,
			Palette: []string{"#22c55e", "#16a34a"}, Values: path,
		})
	}
	return layers
}

// Effects returns the visual effects for the current picture.
func (s *State) Effects() []buildsim.Effect {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []buildsim.Effect{}
	for _, k := range s.sortedRooms() {
		r := s.rooms[k]
		level, name := split(k)
		id := slug(k)
		switch r.state {
		case "fire":
			out = append(out, buildsim.Effect{ID: "fire-" + id, Type: "fire", Label: "Fire", Level: level, Room: name, Radius: 7, Height: 15, Intensity: 0.9})
		case "warning":
			out = append(out, buildsim.Effect{ID: "warning-" + id, Type: "warning", Label: "Armed", Level: level, Room: name, Radius: 6, Height: 15, Intensity: 0.6})
		}
		if r.sprinklerOn {
			out = append(out,
				buildsim.Effect{ID: "sprinkler-" + id, Type: "sprinkler", Label: "Sprinkler", Level: level, Room: name, Radius: 7, Height: 18, Intensity: 0.9},
				buildsim.Effect{ID: "water-" + id, Type: "water", Level: level, Room: name, Radius: 7, Height: 10, Intensity: 0.8})
		}
	}
	return out
}

type ranked struct {
	rank  int // lower first: fire, component down, armed, cancelled
	alert buildsim.Alert
}

// Alerts returns the current alert cards, most important first, capped at
// what BuildSim accepts so a fire is never cut by a crowd of warnings.
func (s *State) Alerts() []buildsim.Alert {
	comps := s.Components() // takes the lock itself

	s.mu.Lock()
	var rs []ranked
	for _, k := range s.sortedRooms() {
		r := s.rooms[k]
		level, name := split(k)
		switch r.state {
		case "fire":
			msg := r.reason
			if r.sprinklerOn {
				msg += "; sprinkler on"
			}
			rs = append(rs, ranked{0, buildsim.Alert{ID: "fire-" + slug(k), Severity: "critical", Title: "Fire detected in " + name, Message: msg, Level: level, Room: name}})
		case "warning":
			msg := r.reason
			if len(r.stale) > 0 {
				msg += "; stale signal: " + strings.Join(r.stale, ", ")
			}
			rs = append(rs, ranked{2, buildsim.Alert{ID: "warning-" + slug(k), Severity: "warning", Title: "Room " + name + " armed", Message: msg, Level: level, Room: name}})
		case "cancelled":
			rs = append(rs, ranked{3, buildsim.Alert{ID: "cancelled-" + slug(k), Severity: "info", Title: "Warning in " + name + " cancelled by the operator", Message: r.reason, Level: level, Room: name}})
		}
	}
	s.mu.Unlock()

	for _, c := range comps {
		if !c.Up {
			rs = append(rs, ranked{1, buildsim.Alert{ID: "component-" + c.Name, Severity: "warning", Title: "Component down: " + c.Name, Message: "no heartbeat"}})
		}
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	out := make([]buildsim.Alert, 0, min(len(rs), maxAlerts))
	for _, r := range rs {
		if len(out) == maxAlerts {
			break
		}
		out = append(out, r.alert)
	}
	return out
}
