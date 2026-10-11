package responder

import (
	"slices"
	"strings"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
)

// Watch follows the people who were in an affected room, so the responders can
// be told who is still inside, who is walking out, who has reached safety and
// who cannot be found. It remembers a person from the first time they are seen
// in an affected room until they are seen in a room that is not affected.
type Watch struct {
	tracked map[string]Occupant
}

// NewWatch returns an empty watch.
func NewWatch() *Watch { return &Watch{tracked: map[string]Occupant{}} }

// Movement is one update of the watch.
type Movement struct {
	InDanger    []Occupant // still in an affected room
	EnRoute     []Occupant // walking out (in no room)
	Unaccounted []Occupant // in no room and not walking out
	Reached     int        // people who reached a room that is not affected, this update
}

// Update compares where tracked people are now with where they were. occ is
// the occupancy by room, ents the entities (may be nil, then nobody can be
// seen walking and anyone in no room is unaccounted for).
func (w *Watch) Update(occ map[string]buildsim.RoomOccupancy, ents []buildsim.Entity, affected map[string]bool) Movement {
	where := map[string]string{} // person id -> room key
	for room, o := range occ {
		for _, p := range o.Persons {
			where[p.ID] = room
		}
	}
	walking := map[string]buildsim.Entity{}
	for _, e := range ents {
		if e.Room == "" && strings.HasPrefix(e.Status, "evacuating") {
			walking[e.ID] = e
		}
	}
	// someone already walking out of an affected room counts, even if we never
	// saw them inside it: the evacuation can start before the first look
	for id, e := range walking {
		if origin, ok := evacuatingFrom(e.Status); ok && affected[e.Level+"/"+origin] {
			if _, known := w.tracked[id]; !known {
				w.tracked[id] = Occupant{ID: id, Name: e.Name, Room: e.Level + "/" + origin}
			}
		}
	}
	// start tracking everyone now in an affected room
	for room, o := range occ {
		if !affected[room] {
			continue
		}
		for _, p := range o.Persons {
			w.tracked[p.ID] = Occupant{ID: p.ID, Name: p.Name, Room: room}
		}
	}

	var m Movement
	ids := make([]string, 0, len(w.tracked))
	for id := range w.tracked {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		o := w.tracked[id]
		switch room, inRoom := where[id]; {
		case inRoom && affected[room]:
			o.Room = room
			m.InDanger = append(m.InDanger, o)
		case inRoom:
			m.Reached++
			delete(w.tracked, id)
		case walking[id].ID != "":
			m.EnRoute = append(m.EnRoute, o)
		default:
			m.Unaccounted = append(m.Unaccounted, o)
		}
	}
	return m
}

// Reset forgets everyone: the incident is over.
func (w *Watch) Reset() { w.tracked = map[string]Occupant{} }

// evacuatingFrom reads the room out of "evacuating from <room> to <room>".
func evacuatingFrom(status string) (string, bool) {
	rest, ok := strings.CutPrefix(status, "evacuating from ")
	if !ok {
		return "", false
	}
	room, _, _ := strings.Cut(rest, " to ")
	return room, room != ""
}
