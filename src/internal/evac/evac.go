// Package evac makes people leave a burning room. The course occupancysim has
// no evacuation hook and is the only writer of the people in BuildSim, and we
// do not modify it, so a proxy sits between them: occupancysim writes to the
// proxy, which passes everything through except people in a room that is on
// fire. Those are walked along a safe route to the nearest safe room, and the
// occupancy counts follow them, so the viewer, the physics and the responder
// all see the room empty. When the fire is over they are handed back.
//
// The engine here is pure (no network, no clock); see proxy.go for the wiring.
package evac

import (
	"hash/fnv"
	"math"
	"strings"
)

// Point is a plan coordinate.
type Point struct{ X, Y float64 }

// Path is a polyline.
type Path []Point

// Length is the walking distance along the path.
func (p Path) Length() float64 {
	var d float64
	for i := 1; i < len(p); i++ {
		d += math.Hypot(p[i].X-p[i-1].X, p[i].Y-p[i-1].Y)
	}
	return d
}

// At returns the point d units along the path and the heading there in
// degrees. Beyond the end it stays on the last point.
func (p Path) At(d float64) (Point, float64) {
	if len(p) == 0 {
		return Point{}, 0
	}
	heading := 0.0
	for i := 1; i < len(p); i++ {
		seg := math.Hypot(p[i].X-p[i-1].X, p[i].Y-p[i-1].Y)
		heading = math.Atan2(p[i].Y-p[i-1].Y, p[i].X-p[i-1].X) * 180 / math.Pi
		if d <= seg && seg > 0 {
			f := d / seg
			return Point{p[i-1].X + f*(p[i].X-p[i-1].X), p[i-1].Y + f*(p[i].Y-p[i-1].Y)}, heading
		}
		d -= seg
	}
	return p[len(p)-1], heading
}

// Candidate is one possible way out: its path, where it ends, and the names of
// the rooms it passes through.
type Candidate struct {
	Path  Path
	Dest  string
	Rooms []string
}

// PickRoute returns the shortest candidate that does not pass through a room
// that is on fire, other than the one being left. BuildSim's router only
// avoids locked doors, so it can send people through a burning room.
func PickRoute(cands []Candidate, level, origin string, fire map[string]bool) (Candidate, bool) {
	var best Candidate
	bestLen, found := 0.0, false
	for _, c := range cands {
		burns := false
		for _, name := range c.Rooms {
			if key := level + "/" + name; key != origin && fire[key] {
				burns = true
				break
			}
		}
		if burns {
			continue
		}
		if l := c.Path.Length(); !found || l < bestLen {
			best, bestLen, found = c, l, true
		}
	}
	return best, found
}

// Router returns the route out of a room on a level and the "<level>/<room>"
// it leads to.
type Router func(level, room string, fire map[string]bool) (Path, string, error)

// Config tunes the walk. Speed and Reaction use the same clock as the `now`
// passed to Apply.
type Config struct {
	Speed    float64 // plan units per second
	Reaction float64 // seconds between the fire and the person starting to walk
	Spread   float64 // plan units: each person walks up to this far off the line, so a crowd is not one dot

	// ReactionSpread and SpeedVariation make a crowd leave over a stretch of
	// time instead of all at once. Each person gets a fixed value from their id:
	// a reaction time between Reaction and Reaction+ReactionSpread seconds, and a
	// speed between Speed*(1-SpeedVariation) and Speed*(1+SpeedVariation).
	ReactionSpread float64
	SpeedVariation float64
}

// unit returns a number in [0,1) that depends only on the person and the salt,
// so the same person always gets the same value.
func unit(id string, salt byte) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte{salt})
	_, _ = h.Write([]byte(id))
	return float64(h.Sum32()%10000) / 10000
}

type evacuee struct {
	origin     string // "<level>/<room>" of the fire room
	start      float64
	path       Path
	dest       string
	name, icon string
	phase      int // 0 waiting, 1 walking, 2 arrived
	at         Point
}

type routeEntry struct {
	path    Path
	dest    string
	failed  bool
	checked float64
}

// Engine tracks who is evacuating. Not safe for concurrent use: the proxy
// serialises calls.
type Engine struct {
	cfg      Config
	route    Router
	evacuees map[string]*evacuee
	routes   map[string]routeEntry
}

// New returns an engine.
func New(cfg Config, route Router) *Engine {
	return &Engine{cfg: cfg, route: route, evacuees: map[string]*evacuee{}, routes: map[string]routeEntry{}}
}

const retryAfter = 10.0 // seconds before asking again for a route that was not found

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func (e *Engine) routeFor(level, room, key string, now float64, fire map[string]bool) (routeEntry, bool) {
	if r, ok := e.routes[key]; ok && (!r.failed || now-r.checked < retryAfter) {
		return r, !r.failed
	}
	path, dest, err := e.route(level, room, fire)
	r := routeEntry{path: path, dest: dest, failed: err != nil || len(path) == 0, checked: now}
	e.routes[key] = r
	return r, !r.failed
}

// Changed reports a version that moves whenever someone starts, stops or
// finishes walking, so the proxy knows when to republish occupancy.
func (e *Engine) Changed() int {
	n := 0
	for _, v := range e.evacuees {
		n = n*3 + v.phase + 1
	}
	return n + len(e.evacuees)*1000
}

// Apply returns the entities with evacuees rewritten. fire holds the
// "<level>/<room>" keys of rooms whose response is on.
func (e *Engine) Apply(entities []map[string]any, now float64, fire map[string]bool) []map[string]any {
	for id, v := range e.evacuees {
		if !fire[v.origin] {
			delete(e.evacuees, id) // fire over: hand the person back
		}
	}
	for k := range e.routes {
		if !fire[k] {
			delete(e.routes, k)
		}
	}
	out := make([]map[string]any, len(entities))
	for i, ent := range entities {
		out[i] = ent
		id := str(ent, "id")
		level, room := str(ent, "level"), str(ent, "room")
		key := level + "/" + room
		v, known := e.evacuees[id]
		if !known {
			if room == "" || !fire[key] {
				continue
			}
			r, ok := e.routeFor(level, room, key, now, fire)
			if !ok {
				continue
			}
			v = &evacuee{origin: key, start: now, path: r.path, dest: r.dest, name: str(ent, "name"), icon: str(ent, "type")}
			e.evacuees[id] = v
		}
		reaction := e.cfg.Reaction + unit(id, 'r')*e.cfg.ReactionSpread
		speed := e.cfg.Speed * (1 + (2*unit(id, 's')-1)*e.cfg.SpeedVariation)
		walked := (now - v.start - reaction) * speed
		if walked <= 0 {
			v.phase = 0
			continue
		}
		cp := make(map[string]any, len(ent)+1)
		for k, val := range ent {
			cp[k] = val
		}
		p, heading := v.path.At(walked)
		if e.cfg.Spread > 0 {
			h := fnv.New32a()
			_, _ = h.Write([]byte(id))
			sum := h.Sum32()
			p.X += (float64(sum%201)/100 - 1) * e.cfg.Spread
			p.Y += (float64((sum/201)%201)/100 - 1) * e.cfg.Spread
		}
		cp["position"] = []float64{p.X, p.Y}
		cp["heading"] = heading
		v.at = p
		if walked >= v.path.Length() {
			v.phase = 2
			_, destRoom, _ := strings.Cut(v.dest, "/")
			cp["room"] = destRoom
			cp["status"] = "evacuated to " + destRoom
		} else {
			v.phase = 1
			delete(cp, "room") // walking: anchored to no room
			_, destRoom, _ := strings.Cut(v.dest, "/")
			cp["status"] = "evacuating from " + room + " to " + destRoom
		}
		out[i] = cp
	}
	return out
}

// ApplyOccupancy rewrites the occupancy map to match: walkers are in no room,
// arrivals are counted in the room they reached.
func (e *Engine) ApplyOccupancy(occ map[string]any) map[string]any {
	moved := map[string]bool{}
	for id, v := range e.evacuees {
		if v.phase > 0 {
			moved[id] = true
		}
	}
	out := make(map[string]any, len(occ))
	for key, val := range occ {
		room, ok := val.(map[string]any)
		if !ok {
			out[key] = val
			continue
		}
		persons, _ := room["persons"].([]any)
		keep := make([]any, 0, len(persons))
		for _, p := range persons {
			if pm, ok := p.(map[string]any); ok && moved[str(pm, "id")] {
				continue
			}
			keep = append(keep, p)
		}
		if len(keep) == 0 {
			continue
		}
		out[key] = map[string]any{"persons": keep, "aliens": room["aliens"]}
	}
	for id, v := range e.evacuees {
		if v.phase != 2 {
			continue
		}
		room, _ := out[v.dest].(map[string]any)
		if room == nil {
			room = map[string]any{"persons": []any{}, "aliens": []any{}}
		}
		persons, _ := room["persons"].([]any)
		out[v.dest] = map[string]any{
			"persons": append(persons, map[string]any{"id": id, "name": v.name, "icon": v.icon, "position": []float64{v.at.X, v.at.Y}}),
			"aliens":  room["aliens"],
		}
	}
	return out
}
