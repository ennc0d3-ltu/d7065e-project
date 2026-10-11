package physics

import (
	"sync"
	"testing"
)

func testEngine(scen ...Scenario) *Engine {
	return NewEngine(Config{
		Params:    DefaultParams(),
		Yields:    DefaultYields(),
		Sprinkler: DefaultSprinkler(),
		Rooms: []RoomSpec{
			{Name: "level0/A109", Volume: 150},
			{Name: "level0/A110", Volume: 150},
			{Name: "level0/B200", Volume: 150}, // no door to the others
		},
		Doors:     []NamedDoor{{A: "level0/A109", B: "level0/A110", Openness: 1}},
		Scenarios: scen,
	})
}

func fire(room int) Scenario {
	return Scenario{Kind: KindFire, Room: room, T0: 0, Alpha: AlphaMedium}
}

func advance(e *Engine, secs int) {
	for range secs {
		e.Tick(1)
	}
}

func TestTickAdvancesSimTime(t *testing.T) {
	e := testEngine()
	advance(e, 5)
	approx(t, e.Time(), 5, 1e-9)
}

func TestUnknownRoomIsReportedMissing(t *testing.T) {
	e := testEngine()
	if _, ok := e.Truth("level9/nope"); ok {
		t.Fatal("unknown room reported present")
	}
}

func TestFireWarmsItsRoomAndLeavesUnconnectedRoomAlone(t *testing.T) {
	e := testEngine(fire(0))
	advance(e, 120)
	hot, _ := e.Truth("level0/A109")
	cold, _ := e.Truth("level0/B200")
	if hot.T-20 < 10 || hot.K < 0.1 {
		t.Fatalf("fire room not detectable: %+v", hot)
	}
	if cold.K != 0 || cold.T-20 > 0.01 {
		t.Fatalf("unconnected room was affected: %+v", cold)
	}
}

func TestSmokeReachesRoomBehindAnOpenDoor(t *testing.T) {
	e := testEngine(fire(0))
	advance(e, 180)
	next, _ := e.Truth("level0/A110")
	if next.K <= 0.01 {
		t.Fatalf("smoke did not spread through the open door: K=%v", next.K)
	}
}

func TestSprinklerStateFromOutsideSuppressesTheFire(t *testing.T) {
	without := testEngine(fire(0))
	with := testEngine(fire(0))
	advance(without, 120)
	advance(with, 60)
	with.SetSprinkler("level0/A109", 1) // the actuator process reports it reached "on"
	advance(with, 60)
	advance(without, 600)
	advance(with, 600)
	a, _ := without.Truth("level0/A109")
	b, _ := with.Truth("level0/A109")
	if b.T >= a.T-20 {
		t.Fatalf("sprinkler had little effect: with=%v without=%v", b.T, a.T)
	}
}

func TestSettingSprinklerOnUnknownRoomIsIgnored(t *testing.T) {
	e := testEngine()
	e.SetSprinkler("level0/NOPE", 1) // must not panic
}

func TestEngineIsSafeForConcurrentReadAndTick(t *testing.T) {
	e := testEngine(fire(0))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		advance(e, 200)
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			e.Truth("level0/A109")
			e.SetSprinkler("level0/A109", 0)
		}
	}()
	wg.Wait()
}
