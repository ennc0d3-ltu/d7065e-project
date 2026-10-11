package responder

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

func person(id, name string) buildsim.Person { return buildsim.Person{ID: id, Name: name} }

func occOf(rooms map[string][]buildsim.Person) map[string]buildsim.RoomOccupancy {
	out := map[string]buildsim.RoomOccupancy{}
	for k, p := range rooms {
		out[k] = buildsim.RoomOccupancy{Persons: p}
	}
	return out
}

var fire = map[string]bool{"level0/A1008": true}

func TestPeopleInTheFireRoomAreTrackedAndStillInDanger(t *testing.T) {
	w := NewWatch()
	occ := occOf(map[string][]buildsim.Person{"level0/A1008": {person("a", "Ann")}})
	res := w.Update(occ, nil, fire)
	if len(res.InDanger) != 1 || len(res.EnRoute) != 0 || len(res.Unaccounted) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestSomeoneWalkingOutIsEnRouteNotMissing(t *testing.T) {
	w := NewWatch()
	w.Update(occOf(map[string][]buildsim.Person{"level0/A1008": {person("a", "Ann")}}), nil, fire)
	ents := []buildsim.Entity{{ID: "a", Name: "Ann", Level: "level0", Status: "evacuating from A1008 to A1016"}}
	res := w.Update(occOf(nil), ents, fire)
	if len(res.EnRoute) != 1 || res.EnRoute[0].ID != "a" || len(res.Unaccounted) != 0 || len(res.InDanger) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestSomeoneWhoReachedASafeRoomIsAccountedForAndForgotten(t *testing.T) {
	w := NewWatch()
	w.Update(occOf(map[string][]buildsim.Person{"level0/A1008": {person("a", "Ann")}}), nil, fire)
	res := w.Update(occOf(map[string][]buildsim.Person{"level0/A1016": {person("a", "Ann")}}), nil, fire)
	if len(res.Unaccounted)+len(res.EnRoute)+len(res.InDanger) != 0 || res.Reached != 1 {
		t.Fatalf("%+v", res)
	}
	// and a later disappearance is not blamed on the fire
	if res := w.Update(occOf(nil), nil, fire); len(res.Unaccounted) != 0 {
		t.Fatalf("a person already safe was reported unaccounted: %+v", res)
	}
}

func TestSomeoneWhoVanishesIsUnaccountedFor(t *testing.T) {
	w := NewWatch()
	w.Update(occOf(map[string][]buildsim.Person{"level0/A1008": {person("a", "Ann")}}), nil, fire)
	res := w.Update(occOf(nil), nil, fire) // in no room, not walking out
	if len(res.Unaccounted) != 1 || res.Unaccounted[0].Name != "Ann" {
		t.Fatalf("%+v", res)
	}
}

func TestAnUnaccountedPersonIsFoundAgainWhenTheyAppear(t *testing.T) {
	w := NewWatch()
	w.Update(occOf(map[string][]buildsim.Person{"level0/A1008": {person("a", "Ann")}}), nil, fire)
	w.Update(occOf(nil), nil, fire)
	res := w.Update(occOf(map[string][]buildsim.Person{"level0/A1016": {person("a", "Ann")}}), nil, fire)
	if len(res.Unaccounted) != 0 || res.Reached != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestPeopleOnlyNearTheFireAreNotTracked(t *testing.T) {
	w := NewWatch()
	res := w.Update(occOf(map[string][]buildsim.Person{"level0/A1000": {person("b", "Bob")}}), nil, fire)
	if len(res.InDanger)+len(res.EnRoute)+len(res.Unaccounted) != 0 {
		t.Fatalf("%+v", res)
	}
	if res := w.Update(occOf(nil), nil, fire); len(res.Unaccounted) != 0 {
		t.Fatal("a person who was never in danger was reported unaccounted for")
	}
}

type fakeEnts struct {
	ents []buildsim.Entity
	err  error
}

func (f fakeEnts) GetEntities(context.Context) ([]buildsim.Entity, error) { return f.ents, f.err }

func TestDispatchCarriesEnRouteAndUnaccounted(t *testing.T) {
	occ := &fakeOcc{occ: map[string]buildsim.RoomOccupancy{"level0/A1008": {Persons: []buildsim.Person{person("a", "Ann"), person("c", "Cy")}}}}
	pub := &capture{}
	s := New(Config{Safe: []string{"level0/A1016"}, MinInterval: 0}, occ, fakeRoutes{}, pub)
	ents := &fakeEnts{}
	s.WithEntities(ents)
	if err := s.HandleDecision(context.Background(), "decisions/level0/A1008", mustJSON(proto.Decision{Room: "level0/A1008", State: "fire"})); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), 10) // both in the room
	// Ann walks out, Cy disappears
	occ.occ = map[string]buildsim.RoomOccupancy{}
	ents.ents = []buildsim.Entity{{ID: "a", Name: "Ann", Level: "level0", Status: "evacuating from A1008 to A1016"}}
	s.Tick(context.Background(), 40)
	ds := pub.dispatches(t)
	last := ds[len(ds)-1]
	if len(last.EnRoute) != 1 || last.EnRoute[0].Name != "Ann" || len(last.Unaccounted) != 1 || last.Unaccounted[0].Name != "Cy" {
		t.Fatalf("last dispatch = %+v", last)
	}
}

func TestWithoutAnEntitySourceNothingChangesForTheResponder(t *testing.T) {
	occ := &fakeOcc{occ: map[string]buildsim.RoomOccupancy{"level0/A1008": {Persons: []buildsim.Person{person("a", "Ann")}}}}
	pub := &capture{}
	s := New(Config{Safe: []string{"level0/A1016"}}, occ, fakeRoutes{}, pub)
	_ = s.HandleDecision(context.Background(), "d", mustJSON(proto.Decision{Room: "level0/A1008", State: "fire"}))
	s.Tick(context.Background(), 10)
	last := pub.dispatches(t)[0]
	if len(last.InDanger) != 1 || len(last.EnRoute) != 0 || len(last.Unaccounted) != 0 {
		t.Fatalf("%+v", last)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestSomeoneFirstSeenWalkingOutOfAnAffectedRoomIsEnRoute(t *testing.T) {
	// the evacuation can start before the responder's first look at the room
	w := NewWatch()
	ents := []buildsim.Entity{
		{ID: "a", Name: "Ann", Level: "level0", Status: "evacuating from A1008 to A1016"},
		{ID: "z", Name: "Zed", Level: "level0", Status: "evacuating from A9999 to A1016"}, // not our fire
	}
	res := w.Update(occOf(nil), ents, fire)
	if len(res.EnRoute) != 1 || res.EnRoute[0].Name != "Ann" || res.EnRoute[0].Room != "level0/A1008" {
		t.Fatalf("%+v", res)
	}
	if len(res.Unaccounted) != 0 {
		t.Fatalf("%+v", res)
	}
}
