package evac

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

// a straight route 100 units along x, then 50 along y
var route = Path{{10, 10}, {110, 10}, {110, 60}}

func TestPathLengthAndPointAt(t *testing.T) {
	if got := route.Length(); got != 150 {
		t.Fatalf("length = %v", got)
	}
	p, h := route.At(50)
	if p != (Point{60, 10}) || math.Abs(h-0) > 1e-9 {
		t.Fatalf("at 50 = %v heading %v", p, h)
	}
	p, h = route.At(125) // 25 units into the second leg, heading +y
	if p != (Point{110, 35}) || math.Abs(h-90) > 1e-9 {
		t.Fatalf("at 125 = %v heading %v", p, h)
	}
	if p, _ := route.At(1e9); p != (Point{110, 60}) {
		t.Fatalf("beyond the end = %v, want the last point", p)
	}
}

func router(calls *int) Router {
	return func(level, room string, _ map[string]bool) (Path, string, error) {
		*calls++
		if room == "nowhere" {
			return nil, "", errors.New("no route")
		}
		return route, "level0/A1016", nil
	}
}

func person(id, room string) map[string]any {
	return map[string]any{"id": id, "name": "N " + id, "type": "man", "level": "level0", "room": room, "position": []any{10.0, 10.0}, "status": "student"}
}

func newEng(calls *int) *Engine {
	return New(Config{Speed: 10, Reaction: 5}, router(calls))
}

func TestPeopleOutsideAFireRoomAreUntouched(t *testing.T) {
	var n int
	e := newEng(&n)
	in := []map[string]any{person("a", "A1000")}
	out := e.Apply(in, 100, map[string]bool{"level0/A1008": true})
	if out[0]["room"] != "A1000" || out[0]["status"] != "student" {
		t.Fatalf("an unaffected person was changed: %v", out[0])
	}
}

func TestPeopleInAFireRoomStartWalkingAfterTheirReactionTime(t *testing.T) {
	var n int
	e := newEng(&n)
	fire := map[string]bool{"level0/A1008": true}
	in := func() []map[string]any { return []map[string]any{person("a", "A1008")} }
	e.Apply(in(), 100, fire)
	if out := e.Apply(in(), 103, fire); out[0]["room"] != "A1008" { // still reacting
		t.Fatalf("moved before reacting: %v", out[0])
	}
	out := e.Apply(in(), 110, fire) // 5 s reaction, then 5 s walking at 10 units/s = 50 units
	if _, inRoom := out[0]["room"]; inRoom {
		t.Fatalf("a walking person must not be anchored to a room: %v", out[0])
	}
	pos := out[0]["position"].([]float64)
	if pos[0] != 60 || pos[1] != 10 {
		t.Fatalf("position = %v, want [60 10]", pos)
	}
	if s, _ := out[0]["status"].(string); s == "student" || s == "" {
		t.Fatalf("status not updated: %q", s)
	}
}

func TestEvacueesArriveAtTheSafeRoomAndStayThereWhileTheFireBurns(t *testing.T) {
	var n int
	e := newEng(&n)
	fire := map[string]bool{"level0/A1008": true}
	in := func() []map[string]any { return []map[string]any{person("a", "A1008")} }
	e.Apply(in(), 100, fire)
	out := e.Apply(in(), 300, fire) // long enough to finish a 150 unit route
	if out[0]["room"] != "A1016" {
		t.Fatalf("arrived person room = %v, want A1016", out[0]["room"])
	}
	again := e.Apply(in(), 400, fire)
	if again[0]["room"] != "A1016" {
		t.Fatal("the person walked back into the fire")
	}
}

func TestEvacueesAreReleasedWhenTheFireIsOver(t *testing.T) {
	var n int
	e := newEng(&n)
	fire := map[string]bool{"level0/A1008": true}
	e.Apply([]map[string]any{person("a", "A1008")}, 100, fire)
	e.Apply([]map[string]any{person("a", "A1008")}, 300, fire)
	out := e.Apply([]map[string]any{person("a", "A1008")}, 400, map[string]bool{})
	if out[0]["room"] != "A1008" || out[0]["status"] != "student" {
		t.Fatalf("not handed back to the people simulator: %v", out[0])
	}
}

func TestTheRouteIsLookedUpOncePerRoomNotPerPerson(t *testing.T) {
	var n int
	e := newEng(&n)
	in := []map[string]any{person("a", "A1008"), person("b", "A1008"), person("c", "A1008")}
	e.Apply(in, 100, map[string]bool{"level0/A1008": true})
	if n != 1 {
		t.Fatalf("route lookups = %d, want 1", n)
	}
}

func TestWithNoRouteThePeopleStayAndNothingBreaks(t *testing.T) {
	var n int
	e := newEng(&n)
	in := []map[string]any{person("a", "nowhere")}
	out := e.Apply(in, 100, map[string]bool{"level0/nowhere": true})
	if out[0]["room"] != "nowhere" {
		t.Fatalf("moved without a route: %v", out[0])
	}
}

func TestOccupancyFollowsThePeople(t *testing.T) {
	var n int
	e := newEng(&n)
	fire := map[string]bool{"level0/A1008": true}
	ents := func() []map[string]any { return []map[string]any{person("a", "A1008"), person("b", "A1000")} }
	occ := func() map[string]any {
		return map[string]any{
			"level0/A1008": map[string]any{"persons": []any{map[string]any{"id": "a", "name": "N a", "icon": "man", "position": []any{10.0, 10.0}}}, "aliens": []any{}},
			"level0/A1000": map[string]any{"persons": []any{map[string]any{"id": "b", "name": "N b", "icon": "man"}}, "aliens": []any{}},
		}
	}
	e.Apply(ents(), 100, fire)
	// walking: leaves the burning room's count, is in no room yet
	e.Apply(ents(), 110, fire)
	mid := e.ApplyOccupancy(occ())
	if _, still := mid["level0/A1008"]; still {
		t.Fatalf("the emptied room is still listed: %v", mid["level0/A1008"])
	}
	if _, ok := mid["level0/A1000"]; !ok {
		t.Fatal("an unaffected room was dropped")
	}
	// arrived: counted in the safe room
	e.Apply(ents(), 300, fire)
	end := e.ApplyOccupancy(occ())
	safe, ok := end["level0/A1016"].(map[string]any)
	if !ok || len(safe["persons"].([]any)) != 1 {
		t.Fatalf("safe room occupancy = %v", end["level0/A1016"])
	}
}

func TestPickRouteTakesTheShortestRouteThatAvoidsBurningRooms(t *testing.T) {
	short := Candidate{Path: Path{{0, 0}, {10, 0}}, Dest: "level0/A1016", Rooms: []string{"A1008", "A1009", "A1016"}}
	long := Candidate{Path: Path{{0, 0}, {50, 0}}, Dest: "level0/A1123", Rooms: []string{"A1008", "corridor", "A1123"}}
	got, ok := PickRoute([]Candidate{long, short}, "level0", "level0/A1008", map[string]bool{})
	if !ok || got.Dest != "level0/A1016" {
		t.Fatalf("no fire elsewhere: got %v %v, want the short route", got.Dest, ok)
	}
	got, ok = PickRoute([]Candidate{long, short}, "level0", "level0/A1008", map[string]bool{"level0/A1009": true})
	if !ok || got.Dest != "level0/A1123" {
		t.Fatalf("A1009 burns: got %v %v, want the longer way round", got.Dest, ok)
	}
}

func TestPickRouteIgnoresTheRoomTheyAreLeaving(t *testing.T) {
	c := Candidate{Path: Path{{0, 0}, {10, 0}}, Dest: "level0/A1016", Rooms: []string{"A1008", "A1016"}}
	if _, ok := PickRoute([]Candidate{c}, "level0", "level0/A1008", map[string]bool{"level0/A1008": true}); !ok {
		t.Fatal("the origin room is on fire by definition; it must not rule the route out")
	}
}

func TestPickRouteWithEveryRouteThroughFireIsNoRoute(t *testing.T) {
	c := Candidate{Path: Path{{0, 0}, {10, 0}}, Dest: "level0/A1016", Rooms: []string{"A1008", "A1009", "A1016"}}
	if _, ok := PickRoute([]Candidate{c}, "level0", "level0/A1008", map[string]bool{"level0/A1009": true}); ok {
		t.Fatal("walked people through a burning room")
	}
}

func TestWithSpreadPeopleFromOneRoomDoNotStackOnOnePoint(t *testing.T) {
	var n int
	e := New(Config{Speed: 10, Reaction: 0, Spread: 3}, router(&n))
	fire := map[string]bool{"level0/A1008": true}
	in := func() []map[string]any {
		return []map[string]any{person("a", "A1008"), person("b", "A1008"), person("c", "A1008")}
	}
	e.Apply(in(), 100, fire)
	out := e.Apply(in(), 105, fire)
	seen := map[[2]float64]bool{}
	for _, o := range out {
		p := o["position"].([]float64)
		seen[[2]float64{p[0], p[1]}] = true
		if math.Abs(p[0]-60) > 3 || math.Abs(p[1]-10) > 3 {
			t.Fatalf("spread too wide: %v", p)
		}
	}
	if len(seen) < 2 {
		t.Fatalf("everyone is on the same point: %v", seen)
	}
	// and the same person always gets the same offset, or they would jitter
	again := e.Apply(in(), 105, fire)
	if out[0]["position"].([]float64)[0] != again[0]["position"].([]float64)[0] {
		t.Fatal("a person's offset changed between calls")
	}
}

func TestTheStatusNamesTheRoomNotTheLevelPath(t *testing.T) {
	var n int
	e := newEng(&n)
	e.Apply([]map[string]any{person("a", "A1008")}, 100, map[string]bool{"level0/A1008": true})
	out := e.Apply([]map[string]any{person("a", "A1008")}, 110, map[string]bool{"level0/A1008": true})
	if got := out[0]["status"]; got != "evacuating from A1008 to A1016" {
		t.Fatalf("status = %q", got)
	}
}

func crowd(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range out {
		out[i] = person(fmt.Sprintf("p%d", i), "A1008")
	}
	return out
}

func TestWithAReactionSpreadPeopleDoNotAllStartWalkingTogether(t *testing.T) {
	var n int
	e := New(Config{Speed: 10, Reaction: 5, ReactionSpread: 10}, router(&n))
	fire := map[string]bool{"level0/A1008": true}
	e.Apply(crowd(40), 100, fire)
	out := e.Apply(crowd(40), 108, fire) // the earliest start at 105 s, the latest at 115 s
	waiting, walking := 0, 0
	for _, o := range out {
		if _, inRoom := o["room"]; inRoom {
			waiting++
		} else {
			walking++
		}
	}
	if waiting == 0 || walking == 0 {
		t.Fatalf("everyone moved together: %d still waiting, %d walking", waiting, walking)
	}
	// nobody starts before the base reaction time
	for _, o := range e.Apply(crowd(40), 104, fire) {
		if _, inRoom := o["room"]; !inRoom {
			t.Fatalf("someone left before the base reaction time: %v", o)
		}
	}
}

func TestWithSpeedVariationPeopleWalkAtDifferentSpeeds(t *testing.T) {
	var n int
	e := New(Config{Speed: 10, Reaction: 0, SpeedVariation: 0.3}, router(&n))
	fire := map[string]bool{"level0/A1008": true}
	e.Apply(crowd(40), 100, fire)
	out := e.Apply(crowd(40), 105, fire) // 50 units at the nominal speed, 35 to 65 with the variation
	seen := map[float64]bool{}
	for _, o := range out {
		x := o["position"].([]float64)[0] - 10 // distance along the first leg
		if x < 35-1e-9 || x > 65+1e-9 {
			t.Fatalf("walked %v units, outside the 35 to 65 band", x)
		}
		seen[x] = true
	}
	if len(seen) < 5 {
		t.Fatalf("people walk at almost the same speed: %d distinct distances", len(seen))
	}
}
