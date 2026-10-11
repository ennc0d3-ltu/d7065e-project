package physics

import "testing"

// noDecay returns test params with ventilation and settling off so exchange
// can be checked in isolation.
func noDecay() Params {
	p := testParams()
	p.LamVent, p.LamSet = 0, 0
	p.KLoss = 0
	return p
}

func TestOpenDoorSpreadsSmokeToNeighbour(t *testing.T) {
	rooms := []Room{{Volume: 100, T: 20, K: 1}, {Volume: 100, T: 20}}
	doors := []Door{{A: 0, B: 1, Openness: 1}}
	got := Step(rooms, doors, noDecay(), make([]Inputs, 2), 1)
	// dK1 = beta*a/V * (K0-K1) = 1/100 * 1 = 0.01
	approx(t, got[1].K, 0.01, 1e-12)
	approx(t, got[0].K, 0.99, 1e-12)
}

func TestExchangeConservesVolumeWeightedSmoke(t *testing.T) {
	rooms := []Room{{Volume: 50, T: 20, K: 2}, {Volume: 150, T: 20}}
	doors := []Door{{A: 0, B: 1, Openness: 1}}
	total := func(rs []Room) float64 { return rs[0].Volume*rs[0].K + rs[1].Volume*rs[1].K }
	before := total(rooms)
	for i := 0; i < 500; i++ {
		rooms = Step(rooms, doors, noDecay(), make([]Inputs, 2), 1)
	}
	approx(t, total(rooms), before, 1e-6)
	// and the rooms converge toward one concentration
	approx(t, rooms[0].K, rooms[1].K, 0.05)
}

func TestClosedDoorSlowsSpread(t *testing.T) {
	open := []Door{{A: 0, B: 1, Openness: 1}}
	closed := []Door{{A: 0, B: 1, Openness: 0.02}}
	mk := func() []Room { return []Room{{Volume: 100, T: 20, K: 1}, {Volume: 100, T: 20}} }
	o := Step(mk(), open, noDecay(), make([]Inputs, 2), 1)
	c := Step(mk(), closed, noDecay(), make([]Inputs, 2), 1)
	approx(t, c[1].K, o[1].K*0.02, 1e-12)
}

func TestHeatAndCOAlsoExchange(t *testing.T) {
	rooms := []Room{{Volume: 100, T: 80, C: 100}, {Volume: 100, T: 20}}
	doors := []Door{{A: 0, B: 1, Openness: 1}}
	got := Step(rooms, doors, noDecay(), make([]Inputs, 2), 1)
	approx(t, got[1].T, 20+0.01*60, 1e-9)
	approx(t, got[1].C, 1, 1e-9)
}

func TestSmallRoomDoesNotOvershoot(t *testing.T) {
	// beta*dt/V = 2 would oscillate with a single explicit step.
	rooms := []Room{{Volume: 0.5, T: 20, K: 1}, {Volume: 0.5, T: 20}}
	doors := []Door{{A: 0, B: 1, Openness: 1}}
	got := Step(rooms, doors, noDecay(), make([]Inputs, 2), 1)
	for i, r := range got {
		if r.K < 0 || r.K > 1 {
			t.Fatalf("room %d K=%v outside [0,1]", i, r.K)
		}
	}
	if got[0].K < got[1].K {
		t.Fatalf("exchange overshot: K0=%v < K1=%v", got[0].K, got[1].K)
	}
}

func TestSourceInOneRoomReachesNeighbourOnlyThroughDoor(t *testing.T) {
	rooms := []Room{{Volume: 100, T: 20}, {Volume: 100, T: 20}}
	in := []Inputs{{Smoke: 10}, {}}
	got := Step(rooms, nil, testParams(), in, 1) // no door
	if got[1].K != 0 {
		t.Fatalf("smoke leaked without a door: K1=%v", got[1].K)
	}
	approx(t, got[0].K, 0.1, 1e-12)
}
