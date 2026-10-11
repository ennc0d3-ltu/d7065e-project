package responder

import (
	"context"
	"errors"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
)

func people(names ...string) buildsim.RoomOccupancy {
	var o buildsim.RoomOccupancy
	for _, n := range names {
		o.Persons = append(o.Persons, buildsim.Person{ID: "id-" + n, Name: n})
	}
	return o
}

func set(rooms ...string) map[string]bool {
	m := map[string]bool{}
	for _, r := range rooms {
		m[r] = true
	}
	return m
}

func TestOccupantsAreClassifiedByTheirRoom(t *testing.T) {
	occ := map[string]buildsim.RoomOccupancy{
		"level0/A109": people("Alice", "Bob"), // burning
		"level0/A108": people("Carol"),        // next door
		"level0/A200": people("Dave", "Eve"),  // far away
	}
	adjacent := map[string][]string{"level0/A109": {"level0/A108"}}
	c := Classify(occ, set("level0/A109"), adjacent)

	if got := c.InDanger; len(got) != 2 || got[0].Name != "Alice" || got[1].Name != "Bob" {
		t.Fatalf("in danger = %+v", got)
	}
	if got := c.AtRisk; len(got) != 1 || got[0].Name != "Carol" || got[0].Room != "level0/A108" {
		t.Fatalf("at risk = %+v", got)
	}
	if c.Safe != 2 {
		t.Fatalf("safe = %d, want 2", c.Safe)
	}
}

func TestARoomThatIsBothBurningAndAdjacentCountsAsDanger(t *testing.T) {
	occ := map[string]buildsim.RoomOccupancy{"level0/A108": people("Carol")}
	adjacent := map[string][]string{"level0/A109": {"level0/A108"}}
	c := Classify(occ, set("level0/A109", "level0/A108"), adjacent)
	if len(c.InDanger) != 1 || len(c.AtRisk) != 0 {
		t.Fatalf("classification = %+v", c)
	}
}

func TestNoOccupantsMeansNothingToReport(t *testing.T) {
	c := Classify(nil, set("level0/A109"), nil)
	if len(c.InDanger) != 0 || len(c.AtRisk) != 0 || c.Safe != 0 {
		t.Fatalf("classification = %+v", c)
	}
}

func TestClassificationOrderIsStable(t *testing.T) {
	occ := map[string]buildsim.RoomOccupancy{
		"level0/B": people("Zed"), "level0/A": people("Amy"),
	}
	a := Classify(occ, set("level0/A", "level0/B"), nil)
	for i := 0; i < 20; i++ {
		b := Classify(occ, set("level0/A", "level0/B"), nil)
		if len(a.InDanger) != len(b.InDanger) || a.InDanger[0] != b.InDanger[0] {
			t.Fatal("classification order changed between calls")
		}
	}
	if a.InDanger[0].Room != "level0/A" {
		t.Fatalf("not sorted by room: %+v", a.InDanger)
	}
}

// fakeRoutes answers from a table of "from>to" keys.
type fakeRoutes map[string]buildsim.Route

func (f fakeRoutes) Route(_ context.Context, fl, fn, tl, tn string) (buildsim.Route, error) {
	r, ok := f[fl+"/"+fn+">"+tl+"/"+tn]
	if !ok {
		return buildsim.Route{}, buildsim.ErrNotFound
	}
	return r, nil
}

func route(dist float64, rooms ...string) buildsim.Route {
	r := buildsim.Route{Distance: dist}
	for _, k := range rooms {
		level, name := splitKey(k)
		r.Path = append(r.Path, buildsim.RoutePoint{Name: name, Level: level})
	}
	return r
}

func TestRouteGoesToTheNearestSafeRoom(t *testing.T) {
	rf := fakeRoutes{
		"level0/A109>level0/EXIT1": route(40, "level0/A109", "level0/CORR", "level0/EXIT1"),
		"level0/A109>level0/EXIT2": route(15, "level0/A109", "level0/CORR", "level0/EXIT2"),
	}
	got, err := PickRoute(context.Background(), rf, "level0/A109", set("level0/A109"), []string{"level0/EXIT1", "level0/EXIT2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.To != "level0/EXIT2" || got.Distance != 15 {
		t.Fatalf("route = %+v, want the nearer EXIT2", got)
	}
}

func TestRouteNeverPassesThroughAnotherBurningRoom(t *testing.T) {
	rf := fakeRoutes{
		// the short way runs through A108, which is also on fire
		"level0/A109>level0/EXIT1": route(10, "level0/A109", "level0/A108", "level0/EXIT1"),
		"level0/A109>level0/EXIT2": route(30, "level0/A109", "level0/CORR", "level0/EXIT2"),
	}
	got, err := PickRoute(context.Background(), rf, "level0/A109", set("level0/A109", "level0/A108"),
		[]string{"level0/EXIT1", "level0/EXIT2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.To != "level0/EXIT2" {
		t.Fatalf("route = %+v: the short route crosses a burning room", got)
	}
}

func TestNoSafeRouteIsReportedNotInvented(t *testing.T) {
	rf := fakeRoutes{
		"level0/A109>level0/EXIT1": route(10, "level0/A109", "level0/A108", "level0/EXIT1"),
	}
	_, err := PickRoute(context.Background(), rf, "level0/A109", set("level0/A109", "level0/A108"),
		[]string{"level0/EXIT1", "level0/EXIT9"}) // EXIT9 is unreachable
	if !errors.Is(err, ErrNoSafeRoute) {
		t.Fatalf("err = %v, want ErrNoSafeRoute", err)
	}
}

func TestTheStartRoomItselfDoesNotDisqualifyARoute(t *testing.T) {
	rf := fakeRoutes{"level0/A109>level0/EXIT1": route(5, "level0/A109", "level0/EXIT1")}
	if _, err := PickRoute(context.Background(), rf, "level0/A109", set("level0/A109"), []string{"level0/EXIT1"}); err != nil {
		t.Fatalf("a route out of the burning room was rejected for starting in it: %v", err)
	}
}

func TestSameNameOnAnotherFloorIsNotTheBurningRoom(t *testing.T) {
	// room names repeat between floors: level1/A108 is not level0/A108
	rf := fakeRoutes{"level0/A109>level0/EXIT1": route(8, "level0/A109", "level1/A108", "level0/EXIT1")}
	if _, err := PickRoute(context.Background(), rf, "level0/A109", set("level0/A109", "level0/A108"), []string{"level0/EXIT1"}); err != nil {
		t.Fatalf("a route through level1/A108 was blocked by a fire in level0/A108: %v", err)
	}
}

func TestNoSafeRoomsConfiguredIsNoRoute(t *testing.T) {
	if _, err := PickRoute(context.Background(), fakeRoutes{}, "level0/A109", set("level0/A109"), nil); !errors.Is(err, ErrNoSafeRoute) {
		t.Fatalf("err = %v, want ErrNoSafeRoute", err)
	}
}

func TestRoomsOnARouteAreListedInOrder(t *testing.T) {
	r := RouteChoice{Path: route(1, "level0/A109", "level0/CORR", "level0/CORR", "level0/EXIT").Path}
	got := r.Rooms()
	want := []string{"level0/A109", "level0/CORR", "level0/EXIT"}
	if len(got) != len(want) {
		t.Fatalf("rooms = %v, want %v (consecutive duplicates collapsed)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rooms = %v, want %v", got, want)
		}
	}
}
