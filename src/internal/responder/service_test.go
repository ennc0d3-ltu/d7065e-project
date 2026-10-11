package responder

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

type fakeOcc struct {
	occ   map[string]buildsim.RoomOccupancy
	err   error
	calls int
}

func (f *fakeOcc) GetOccupancy(context.Context) (map[string]buildsim.RoomOccupancy, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.occ, nil
}

type countingRoutes struct {
	inner fakeRoutes
	calls int
	fail  bool
}

func (c *countingRoutes) Route(ctx context.Context, a, b, d, e string) (buildsim.Route, error) {
	c.calls++
	if c.fail {
		return buildsim.Route{}, errors.New("buildsim down")
	}
	return c.inner.Route(ctx, a, b, d, e)
}

type sent struct {
	topic   string
	payload []byte
}

type capture struct {
	msgs    []sent
	failing int
}

func (c *capture) Publish(_ context.Context, topic string, _ byte, payload []byte) error {
	if c.failing > 0 {
		c.failing--
		return errors.New("broker down")
	}
	c.msgs = append(c.msgs, sent{topic, payload})
	return nil
}

func (c *capture) dispatches(t *testing.T) []proto.Dispatch {
	t.Helper()
	var out []proto.Dispatch
	for _, m := range c.msgs {
		if m.topic == proto.DispatchTopic {
			var d proto.Dispatch
			if err := json.Unmarshal(m.payload, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
	}
	return out
}

func (c *capture) acks(t *testing.T) []proto.DispatchAck {
	t.Helper()
	var out []proto.DispatchAck
	for _, m := range c.msgs {
		if m.topic == proto.DispatchAckTopic {
			var a proto.DispatchAck
			if err := json.Unmarshal(m.payload, &a); err != nil {
				t.Fatal(err)
			}
			out = append(out, a)
		}
	}
	return out
}

func (c *capture) routes(t *testing.T) []proto.RouteMsg {
	t.Helper()
	var out []proto.RouteMsg
	for _, m := range c.msgs {
		if len(m.topic) > 6 && m.topic[:6] == "route/" {
			var r proto.RouteMsg
			if err := json.Unmarshal(m.payload, &r); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
	}
	return out
}

const a109, a108, exit1, exit2 = "level0/A109", "level0/A108", "level0/EXIT1", "level0/EXIT2"

func testRoutes() *countingRoutes {
	return &countingRoutes{inner: fakeRoutes{
		a109 + ">" + exit1: route(20, a109, "level0/CORR", exit1),
		a109 + ">" + exit2: route(60, a109, "level0/CORR", exit2),
		a108 + ">" + exit1: route(25, a108, "level0/CORR", exit1),
	}}
}

func testConfig() Config {
	return Config{
		Safe:        []string{exit1, exit2},
		Adjacent:    map[string][]string{a109: {a108}, a108: {a109}},
		MinInterval: 30,
		AckDelay:    10,
	}
}

func decision(room, state, action string, simTime float64) []byte {
	b, _ := json.Marshal(proto.Decision{Room: room, SimTime: simTime, Tier: "fire", State: state, Action: action, Reason: "r"})
	return b
}

var ctx = context.Background()

func newSvc(occ *fakeOcc, rf RouteFinder, pub Publisher) *Service {
	return New(testConfig(), occ, rf, pub)
}

func TestAFireOpensAnIncidentAndDispatchesWithOccupancyAndARoute(t *testing.T) {
	occ := &fakeOcc{occ: map[string]buildsim.RoomOccupancy{
		a109: people("Alice", "Bob"), a108: people("Carol"), "level0/A200": people("Dave"),
	}}
	pub := &capture{}
	s := newSvc(occ, testRoutes(), pub)
	if err := s.HandleDecision(ctx, "decisions/"+a109, decision(a109, "fire", "actuate", 100)); err != nil {
		t.Fatal(err)
	}
	s.Tick(ctx, 100)

	ds := pub.dispatches(t)
	if len(ds) != 1 {
		t.Fatalf("dispatches = %+v", ds)
	}
	d := ds[0]
	if d.Kind != "fire" || d.SimTime != 100 || len(d.Affected) != 1 || d.Affected[0] != a109 {
		t.Fatalf("dispatch = %+v", d)
	}
	if len(d.InDanger) != 2 || len(d.AtRisk) != 1 || d.Safe != 1 || d.OccupancyStale {
		t.Fatalf("occupancy = in danger %d, at risk %d, safe %d, stale %v", len(d.InDanger), len(d.AtRisk), d.Safe, d.OccupancyStale)
	}
	if len(d.Routes) != 1 || d.Routes[0].To != exit1 || d.Routes[0].From != a109 {
		t.Fatalf("routes = %+v", d.Routes)
	}
	if d.Incident == "" || d.Severity != "critical" {
		t.Fatalf("incident=%q severity=%q", d.Incident, d.Severity)
	}
}

func TestAWarningAloneOpensNoIncident(t *testing.T) {
	pub := &capture{}
	s := newSvc(&fakeOcc{}, testRoutes(), pub)
	_ = s.HandleDecision(ctx, "decisions/"+a109, decision(a109, "warning", "warn", 100))
	s.Tick(ctx, 100)
	if len(pub.dispatches(t)) != 0 {
		t.Fatal("responders were dispatched for a warning")
	}
}

func TestTheRouteIsPublishedForTheDashboard(t *testing.T) {
	pub := &capture{}
	s := newSvc(&fakeOcc{}, testRoutes(), pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	rs := pub.routes(t)
	if len(rs) != 1 || rs[0].From != a109 || len(rs[0].Rooms) != 3 || rs[0].Rooms[2] != exit1 {
		t.Fatalf("routes = %+v", rs)
	}
}

func TestNothingIsRepeatedWhileNothingChanges(t *testing.T) {
	pub := &capture{}
	rf := testRoutes()
	s := newSvc(&fakeOcc{occ: map[string]buildsim.RoomOccupancy{a109: people("Alice")}}, rf, pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	for now := 100.0; now <= 400; now += 5 {
		s.Tick(ctx, now)
	}
	if got := len(pub.dispatches(t)); got != 1 {
		t.Fatalf("dispatches = %d, want 1", got)
	}
	if rf.calls != 2 { // one lookup per safe room, once, not every tick
		t.Fatalf("route lookups = %d, want 2: routes must be recomputed only when the affected set changes", rf.calls)
	}
}

func TestOccupancyChangesAreRateLimited(t *testing.T) {
	pub := &capture{}
	occ := &fakeOcc{occ: map[string]buildsim.RoomOccupancy{a109: people("Alice")}}
	s := newSvc(occ, testRoutes(), pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	occ.occ = map[string]buildsim.RoomOccupancy{a109: people("Alice", "Bob")} // someone walks in
	s.Tick(ctx, 110)                                                          // inside MinInterval (30 s)
	if got := len(pub.dispatches(t)); got != 1 {
		t.Fatalf("dispatches = %d inside the rate limit, want 1", got)
	}
	s.Tick(ctx, 135)
	ds := pub.dispatches(t)
	if len(ds) != 2 || ds[1].Kind != "update" || len(ds[1].InDanger) != 2 {
		t.Fatalf("dispatches = %+v, want an update with both people in danger", ds)
	}
}

func TestAGrowingFireIsNeverDelayedByTheRateLimit(t *testing.T) {
	pub := &capture{}
	s := newSvc(&fakeOcc{}, testRoutes(), pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	_ = s.HandleDecision(ctx, "d", decision(a108, "fire", "actuate", 105)) // spreads
	s.Tick(ctx, 105)
	ds := pub.dispatches(t)
	if len(ds) != 2 || len(ds[1].Affected) != 2 {
		t.Fatalf("dispatches = %+v, want an immediate update naming both rooms", ds)
	}
}

func TestTheAllClearIsDispatchedOnceWhenTheLastRoomClears(t *testing.T) {
	pub := &capture{}
	s := newSvc(&fakeOcc{}, testRoutes(), pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	_ = s.HandleDecision(ctx, "d", decision(a109, "idle", "release", 500))
	s.Tick(ctx, 500)
	s.Tick(ctx, 505)
	ds := pub.dispatches(t)
	if len(ds) != 2 || ds[1].Kind != "all-clear" {
		t.Fatalf("dispatches = %+v", ds)
	}
}

func TestStaleOccupancyIsFlaggedAndTheLastKnownPictureIsUsed(t *testing.T) {
	pub := &capture{}
	occ := &fakeOcc{occ: map[string]buildsim.RoomOccupancy{a109: people("Alice")}}
	s := newSvc(occ, testRoutes(), pub)
	s.Tick(ctx, 50) // no incident yet, but occupancy is observed
	occ.err = errors.New("buildsim down")
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	d := pub.dispatches(t)[0]
	if !d.OccupancyStale {
		t.Fatal("a dispatch built on unreachable occupancy was not flagged stale")
	}
	if len(d.InDanger) != 1 || d.InDanger[0].Name != "Alice" {
		t.Fatalf("last known occupancy not used: %+v", d.InDanger)
	}
}

func TestWithNoOccupancyEverSeenTheDispatchSaysSoRatherThanClaimingEmpty(t *testing.T) {
	pub := &capture{}
	s := newSvc(&fakeOcc{err: errors.New("down")}, testRoutes(), pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	d := pub.dispatches(t)[0]
	if !d.OccupancyStale || !d.OccupancyUnknown {
		t.Fatalf("dispatch = %+v: with no occupancy data it must say unknown, not report an empty building", d)
	}
}

func TestNoSafeRouteIsListedNotHidden(t *testing.T) {
	pub := &capture{}
	rf := &countingRoutes{inner: fakeRoutes{}} // nothing reachable
	s := newSvc(&fakeOcc{}, rf, pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	d := pub.dispatches(t)[0]
	if len(d.NoRoute) != 1 || d.NoRoute[0] != a109 || len(d.Routes) != 0 {
		t.Fatalf("dispatch = %+v", d)
	}
}

func TestTheLastGoodRouteIsKeptWhenRoutingFailsAfterTheFireSpreads(t *testing.T) {
	pub := &capture{}
	rf := testRoutes()
	s := newSvc(&fakeOcc{}, rf, pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	rf.fail = true // BuildSim routing goes down
	_ = s.HandleDecision(ctx, "d", decision("level0/A200", "fire", "actuate", 105))
	s.Tick(ctx, 105)
	last := pub.dispatches(t)[1]
	var kept bool
	for _, r := range last.Routes {
		if r.From == a109 && r.To == exit1 {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("the cached route out of A109 was dropped: %+v", last)
	}
}

func TestAcknowledgementIsRecordedOncePerDispatchAfterTheDelay(t *testing.T) {
	pub := &capture{}
	s := newSvc(&fakeOcc{}, testRoutes(), pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	if len(pub.acks(t)) != 0 {
		t.Fatal("acknowledged instantly")
	}
	s.Tick(ctx, 112) // AckDelay 10
	s.Tick(ctx, 120)
	as := pub.acks(t)
	if len(as) != 1 || as[0].Incident == "" || as[0].SimTime < 110 {
		t.Fatalf("acks = %+v", as)
	}
	if len(s.Acks()) != 1 {
		t.Fatalf("acks logged = %+v", s.Acks())
	}
}

func TestAFailedDispatchPublishIsRetriedOnTheNextTick(t *testing.T) {
	pub := &capture{failing: 1}
	s := newSvc(&fakeOcc{}, testRoutes(), pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100) // the dispatch is lost
	if len(pub.dispatches(t)) != 0 {
		t.Fatal("test setup: the first publish should have failed")
	}
	s.Tick(ctx, 105)
	if len(pub.dispatches(t)) != 1 {
		t.Fatalf("the dispatch was not retried: %+v", pub.msgs)
	}
}

func TestMalformedDecisionIsRejected(t *testing.T) {
	if err := newSvc(&fakeOcc{}, testRoutes(), &capture{}).HandleDecision(ctx, "d", []byte(`nope`)); err == nil {
		t.Fatal("expected an error")
	}
}

func TestARoomWithNoRouteIsRetriedAndTheRouteIsSentOnceItExists(t *testing.T) {
	// a locked door makes the first lookup fail; the door actuator unlocks it a
	// moment later, and the responder must then find the route by itself
	pub := &capture{}
	rf := testRoutes()
	rf.fail = true
	s := newSvc(&fakeOcc{}, rf, pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	if d := pub.dispatches(t)[0]; len(d.NoRoute) != 1 || len(d.Routes) != 0 {
		t.Fatalf("setup: first dispatch = %+v", d)
	}
	rf.fail = false // the door is unlocked
	s.Tick(ctx, 110)
	all := pub.dispatches(t)
	last := all[len(all)-1]
	if len(last.Routes) != 1 || len(last.NoRoute) != 0 {
		t.Fatalf("after the door opened the dispatch is still %+v", last)
	}
}

func TestAMissingRouteIsNotLookedUpOnEveryTick(t *testing.T) {
	pub := &capture{}
	rf := testRoutes()
	rf.fail = true
	s := newSvc(&fakeOcc{}, rf, pub)
	_ = s.HandleDecision(ctx, "d", decision(a109, "fire", "actuate", 100))
	s.Tick(ctx, 100)
	before := rf.calls
	for now := 101.0; now < 104; now++ { // three more ticks within the retry interval
		s.Tick(ctx, now)
	}
	if rf.calls != before {
		t.Fatalf("BuildSim was asked %d more times within the retry interval", rf.calls-before)
	}
}
