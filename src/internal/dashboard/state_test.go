package dashboard

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newState(expected ...string) (*State, *clock) {
	clk := &clock{now: time.Unix(1000, 0)}
	return New(Config{Now: clk.Now, ComponentTTL: 10 * time.Second, Expected: expected}), clk
}

func decide(room, state, tier, action, reason string, stale ...string) proto.Decision {
	return proto.Decision{Room: room, SimTime: 5, Tier: tier, State: state, Action: action, Reason: reason, Stale: stale}
}

func cmd(room, action string) proto.Command {
	return proto.Command{Room: room, Action: action, Reason: "r", Seq: 1, TTL: 30, SimTime: 5}
}

func effectsByType(es []buildsim.Effect, typ string) []buildsim.Effect {
	var out []buildsim.Effect
	for _, e := range es {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func alertByID(as []buildsim.Alert, id string) (buildsim.Alert, bool) {
	for _, a := range as {
		if a.ID == id {
			return a, true
		}
	}
	return buildsim.Alert{}, false
}

func TestEmptyStateProducesEmptyCollections(t *testing.T) {
	s, _ := newState()
	if len(s.Effects()) != 0 || len(s.Alerts()) != 0 {
		t.Fatalf("effects=%v alerts=%v", s.Effects(), s.Alerts())
	}
	layers := s.Layers()
	if len(layers) != 1 || len(layers[0].Values) != 0 {
		t.Fatalf("layers = %+v", layers)
	}
}

func TestRiskLayerRatesRoomsByState(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "held 15s"))
	s.ApplyDecision(decide("level0/A108", "warning", "warning", "warn", "armed"))
	s.ApplyDecision(decide("level0/A107", "idle", "none", "none", ""))
	v := s.Layers()[0].Values
	if v["level0/A109"] != 2 || v["level0/A108"] != 1 || v["level0/A107"] != 0 {
		t.Fatalf("values = %v", v)
	}
	l := s.Layers()[0]
	if l.Minimum != 0 || l.Maximum != 2 || len(l.Palette) != 3 {
		t.Fatalf("layer range or palette wrong: %+v", l)
	}
}

func TestFireRoomShowsAFireEffectWithTheRightPlacement(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "held 15s"))
	fires := effectsByType(s.Effects(), "fire")
	if len(fires) != 1 || fires[0].Level != "level0" || fires[0].Room != "A109" || fires[0].ID != "fire-level0-A109" {
		t.Fatalf("fire effects = %+v", fires)
	}
}

func TestWarningRoomShowsAWarningNotAFire(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A108", "warning", "warning", "warn", "armed"))
	if len(effectsByType(s.Effects(), "fire")) != 0 {
		t.Fatal("a warning room was drawn on fire")
	}
	if len(effectsByType(s.Effects(), "warning")) != 1 {
		t.Fatalf("effects = %+v", s.Effects())
	}
}

func TestSprinklerEffectsFollowTheCommandNotTheDecision(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "r"))
	if len(effectsByType(s.Effects(), "sprinkler")) != 0 {
		t.Fatal("sprinkler drawn before any command")
	}
	s.ApplyCommand(cmd("level0/A109", proto.ActionOn))
	e := s.Effects()
	if len(effectsByType(e, "sprinkler")) != 1 || len(effectsByType(e, "water")) != 1 {
		t.Fatalf("effects = %+v", e)
	}
	s.ApplyCommand(cmd("level0/A109", proto.ActionOff))
	if got := effectsByType(s.Effects(), "sprinkler"); len(got) != 0 {
		t.Fatalf("sprinkler still drawn after an off command: %+v", got)
	}
}

func TestReleaseClearsTheFireEffectAndAlert(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "r"))
	s.ApplyDecision(decide("level0/A109", "idle", "none", "release", "all-clear"))
	if len(effectsByType(s.Effects(), "fire")) != 0 {
		t.Fatal("fire effect survived the all-clear")
	}
	if _, ok := alertByID(s.Alerts(), "fire-level0-A109"); ok {
		t.Fatal("fire alert survived the all-clear")
	}
}

func TestAlertsDescribeFireWarningAndCancel(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "fire tier held 15s"))
	s.ApplyDecision(decide("level0/A108", "warning", "warning", "warn", "armed", "temperature"))
	s.ApplyDecision(decide("level0/A107", "cancelled", "warning", "none", "cancelled by operator"))
	as := s.Alerts()

	fire, ok := alertByID(as, "fire-level0-A109")
	if !ok || fire.Severity != "critical" || fire.Level != "level0" || fire.Room != "A109" || fire.Message == "" {
		t.Fatalf("fire alert = %+v ok=%v", fire, ok)
	}
	warn, ok := alertByID(as, "warning-level0-A108")
	if !ok || warn.Severity != "warning" {
		t.Fatalf("warning alert = %+v", warn)
	}
	if want := "temperature"; !contains(warn.Message, want) {
		t.Fatalf("the stale signal is not named in %q", warn.Message)
	}
	cancelled, ok := alertByID(as, "cancelled-level0-A107")
	if !ok || cancelled.Severity != "info" {
		t.Fatalf("cancel alert = %+v", cancelled)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func layerByID(ls []buildsim.Layer, id string) (buildsim.Layer, bool) {
	for _, l := range ls {
		if l.ID == id {
			return l, true
		}
	}
	return buildsim.Layer{}, false
}

func TestRouteIsDrawnAsASecondLayerOverItsRooms(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "r"))
	s.ApplyRoute(proto.RouteMsg{Incident: "i", From: "level0/A109", To: "level0/EXIT1",
		Rooms: []string{"level0/A109", "level0/CORR", "level0/EXIT1"}})
	l, ok := layerByID(s.Layers(), "evacuation-route")
	if !ok {
		t.Fatalf("no route layer: %+v", s.Layers())
	}
	for _, room := range []string{"level0/CORR", "level0/EXIT1"} {
		if l.Values[room] != 1 {
			t.Errorf("room %s not on the route layer: %v", room, l.Values)
		}
	}
	if _, onRoute := l.Values["level0/A109"]; onRoute {
		t.Errorf("the burning room is the start of the route, not part of the safe path: %v", l.Values)
	}
}

func TestNoRouteLayerWhenThereIsNoRoute(t *testing.T) {
	s, _ := newState()
	if _, ok := layerByID(s.Layers(), "evacuation-route"); ok {
		t.Fatal("a route layer without a route")
	}
}

func TestRouteDisappearsWhenItsRoomStopsBurning(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "r"))
	s.ApplyRoute(proto.RouteMsg{From: "level0/A109", To: "level0/EXIT1", Rooms: []string{"level0/A109", "level0/EXIT1"}})
	s.ApplyDecision(decide("level0/A109", "idle", "none", "release", "all-clear"))
	if _, ok := layerByID(s.Layers(), "evacuation-route"); ok {
		t.Fatal("the route stayed after the fire was released")
	}
}

func TestANewRouteForTheSameRoomReplacesTheOldOne(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "r"))
	s.ApplyRoute(proto.RouteMsg{From: "level0/A109", To: "level0/EXIT1", Rooms: []string{"level0/A109", "level0/OLD", "level0/EXIT1"}})
	s.ApplyRoute(proto.RouteMsg{From: "level0/A109", To: "level0/EXIT2", Rooms: []string{"level0/A109", "level0/NEW", "level0/EXIT2"}})
	l, _ := layerByID(s.Layers(), "evacuation-route")
	if _, old := l.Values["level0/OLD"]; old || l.Values["level0/NEW"] != 1 {
		t.Fatalf("values = %v, want only the new route", l.Values)
	}
}

func TestARefreshKeepsTheReasonOfTheRealDecision(t *testing.T) {
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "fire tier held 15s"))
	refresh := decide("level0/A109", "fire", "fire", "none", "state refresh")
	refresh.Refresh = true
	s.ApplyDecision(refresh)
	a, ok := alertByID(s.Alerts(), "fire-level0-A109")
	if !ok || !contains(a.Message, "fire tier held 15s") || contains(a.Message, "refresh") {
		t.Fatalf("alert = %+v: a refresh must not overwrite why the room is on fire", a)
	}
}

func TestARefreshWithANewStateStillAppliesIt(t *testing.T) {
	// a missed all-clear is repaired by the refresh
	s, _ := newState()
	s.ApplyDecision(decide("level0/A109", "fire", "fire", "actuate", "fire tier held 15s"))
	refresh := decide("level0/A109", "idle", "none", "none", "state refresh")
	refresh.Refresh = true
	s.ApplyDecision(refresh)
	if len(effectsByType(s.Effects(), "fire")) != 0 {
		t.Fatal("the refresh did not clear the missed fire state")
	}
}

func TestComponentsReportDownWhenHeartbeatsStop(t *testing.T) {
	s, clk := newState("detector", "bridge")
	hb := func(p string) proto.Heartbeat { return proto.Heartbeat{Process: p, Seq: 1, SimTime: 1} }
	s.ApplyHeartbeat(hb("detector"))
	s.ApplyHeartbeat(hb("bridge"))
	if down := downNames(s); len(down) != 0 {
		t.Fatalf("down = %v right after heartbeats", down)
	}
	clk.now = clk.now.Add(8 * time.Second)
	s.ApplyHeartbeat(hb("detector")) // only the detector keeps beating
	clk.now = clk.now.Add(8 * time.Second)
	if down := downNames(s); len(down) != 1 || down[0] != "bridge" {
		t.Fatalf("down = %v, want only the bridge", down)
	}
	if _, ok := alertByID(s.Alerts(), "component-bridge"); !ok {
		t.Fatalf("no alert for the dead bridge: %+v", s.Alerts())
	}
	if _, ok := alertByID(s.Alerts(), "component-detector"); ok {
		t.Fatal("alert for a healthy component")
	}
	s.ApplyHeartbeat(hb("bridge")) // recovery
	if down := downNames(s); len(down) != 0 {
		t.Fatalf("down = %v after recovery", down)
	}
}

func downNames(s *State) []string {
	var out []string
	for _, c := range s.Components() {
		if !c.Up {
			out = append(out, c.Name)
		}
	}
	return out
}

func TestAComponentNeverHeardFromIsDownOnceTheGracePeriodPasses(t *testing.T) {
	s, clk := newState("actuator")
	if len(downNames(s)) != 0 {
		t.Fatal("reported down at startup, before it had a chance to beat")
	}
	clk.now = clk.now.Add(11 * time.Second)
	if down := downNames(s); len(down) != 1 || down[0] != "actuator" {
		t.Fatalf("down = %v", down)
	}
}

func TestUnexpectedHeartbeatsAreIgnored(t *testing.T) {
	s, _ := newState("detector")
	s.ApplyHeartbeat(proto.Heartbeat{Process: "mystery", Seq: 1})
	if len(s.Components()) != 1 {
		t.Fatalf("components = %+v", s.Components())
	}
}

func TestAlertsAreCappedAtWhatBuildSimAccepts(t *testing.T) {
	s, _ := newState()
	for i := 0; i < 150; i++ {
		s.ApplyDecision(decide(fmt.Sprintf("level0/R%d", i), "warning", "warning", "warn", "armed"))
	}
	if got := len(s.Alerts()); got != 100 {
		t.Fatalf("alerts = %d, want 100", got)
	}
}

func TestFireAlertsComeBeforeWarningsWhenCapped(t *testing.T) {
	s, _ := newState()
	for i := 0; i < 120; i++ {
		s.ApplyDecision(decide(fmt.Sprintf("level0/R%d", i), "warning", "warning", "warn", "armed"))
	}
	s.ApplyDecision(decide("level0/FIRE", "fire", "fire", "actuate", "r"))
	if _, ok := alertByID(s.Alerts(), "fire-level0-FIRE"); !ok {
		t.Fatal("the fire alert was cut by the cap")
	}
}

func TestStateIsSafeForConcurrentUse(t *testing.T) {
	s, _ := newState("detector")
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				s.ApplyDecision(decide(fmt.Sprintf("level0/R%d", g), "warning", "warning", "warn", "x"))
				s.ApplyCommand(cmd(fmt.Sprintf("level0/R%d", g), proto.ActionOn))
				s.ApplyHeartbeat(proto.Heartbeat{Process: "detector", Seq: uint64(i)})
				_ = s.Layers()
				_ = s.Effects()
				_ = s.Alerts()
			}
		}(g)
	}
	wg.Wait()
}
