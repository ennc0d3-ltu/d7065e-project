// Package responder tells responders what is happening: which rooms are
// affected, who is in danger or at risk, and a route out that does not cross
// a burning room (FR5, FR6, FR9; decision S2). It only reads: occupancy
// belongs to the course occupancysim (D12), and the viewer overlays belong to
// the dashboard, so no two processes ever write the same BuildSim collection.
package responder

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
)

// ErrNoSafeRoute means no route to any safe room avoids the affected rooms.
var ErrNoSafeRoute = errors.New("responder: no safe route")

// Occupant is one person and where they are ("<level>/<room>").
type Occupant struct {
	ID   string
	Name string
	Room string
}

// Classification sorts occupants by exposure. With no evacuee motion in v1
// (S1), "en route" and "unaccounted" cannot be derived: only room and time.
type Classification struct {
	InDanger []Occupant // in an affected room
	AtRisk   []Occupant // in a room next to an affected room
	Safe     int        // everyone else, counted

	// set by Watch when the service can see people walking
	EnRoute, Unaccounted []Occupant
}

func splitKey(k string) (level, name string) {
	level, name, _ = strings.Cut(k, "/")
	return level, name
}

// Classify reads who is where. adjacent maps a room to the rooms that share a
// door with it. Rooms and people come out in a stable order.
func Classify(occ map[string]buildsim.RoomOccupancy, affected map[string]bool, adjacent map[string][]string) Classification {
	atRisk := map[string]bool{}
	for room := range affected {
		for _, n := range adjacent[room] {
			if !affected[n] {
				atRisk[n] = true
			}
		}
	}
	rooms := make([]string, 0, len(occ))
	for r := range occ {
		rooms = append(rooms, r)
	}
	sort.Strings(rooms)

	var c Classification
	for _, r := range rooms {
		for _, p := range occ[r].Persons {
			o := Occupant{ID: p.ID, Name: p.Name, Room: r}
			switch {
			case affected[r]:
				c.InDanger = append(c.InDanger, o)
			case atRisk[r]:
				c.AtRisk = append(c.AtRisk, o)
			default:
				c.Safe++
			}
		}
	}
	return c
}

// RouteFinder computes a walkable route; *buildsim.Client implements it.
type RouteFinder interface {
	Route(ctx context.Context, fromLevel, fromName, toLevel, toName string) (buildsim.Route, error)
}

// RouteChoice is a route to a safe room.
type RouteChoice struct {
	From, To string
	Distance float64
	Path     []buildsim.RoutePoint
}

// Rooms lists the rooms the route passes through, in order, with consecutive
// repeats (corridor waypoints) collapsed.
func (r RouteChoice) Rooms() []string {
	var out []string
	for _, p := range r.Path {
		k := p.Level + "/" + p.Name
		if len(out) == 0 || out[len(out)-1] != k {
			out = append(out, k)
		}
	}
	return out
}

// PickRoute returns the nearest safe room reachable from `from` without
// passing through any other affected room. Room names repeat between floors,
// so every comparison is level-qualified. A route that cannot be computed is
// skipped, and if nothing works the answer is ErrNoSafeRoute, never a guess.
func PickRoute(ctx context.Context, rf RouteFinder, from string, affected map[string]bool, safe []string) (RouteChoice, error) {
	fromLevel, fromName := splitKey(from)
	var best RouteChoice
	found := false
	for _, to := range safe {
		if affected[to] {
			continue
		}
		toLevel, toName := splitKey(to)
		r, err := rf.Route(ctx, fromLevel, fromName, toLevel, toName)
		if err != nil || crossesAffected(r, from, affected) {
			continue
		}
		if !found || r.Distance < best.Distance {
			best = RouteChoice{From: from, To: to, Distance: r.Distance, Path: r.Path}
			found = true
		}
	}
	if !found {
		return RouteChoice{}, ErrNoSafeRoute
	}
	return best, nil
}

func crossesAffected(r buildsim.Route, from string, affected map[string]bool) bool {
	for _, p := range r.Path {
		k := p.Level + "/" + p.Name
		if k != from && affected[k] {
			return true
		}
	}
	return false
}
