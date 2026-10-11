package physics

import "testing"

const tick = 1.0

// run steps a single room under a scenario for secs seconds and returns the
// trajectory (index = second). sprinklerOn/at control a latched sprinkler.
func run(sc Scenario, secs int, sprinklerAt float64) []Room {
	p := DefaultParams()
	y := DefaultYields()
	sp := DefaultSprinkler()
	r := Room{Volume: 150, T: p.TOut}
	traj := make([]Room, 0, secs+1)
	traj = append(traj, r)
	for s := 0; s < secs; s++ {
		t := float64(s)
		spr := 0.0
		if sprinklerAt >= 0 && t >= sprinklerAt {
			spr = 1
		}
		in := sc.InputsAt(t, spr, sprinklerAt, sp, y)
		in.Sprinkler = spr
		r = StepRoom(r, p, in, tick)
		traj = append(traj, r)
	}
	return traj
}

func TestFireInputsScaleWithHeat(t *testing.T) {
	y := Yields{GS: 0.018, GCO: 5.3}
	in := FireInputs(100, y)
	approx(t, in.Heat, 100, 1e-9)
	approx(t, in.Smoke, 1.8, 1e-9)
	approx(t, in.CO, 530, 1e-9)
}

func TestScenarioIsQuietOutsideItsWindow(t *testing.T) {
	for _, sc := range []Scenario{
		{Kind: KindCooking, T0: 100, Duration: 60},
		{Kind: KindDust, T0: 100, Duration: 60},
	} {
		before := sc.InputsAt(99, 0, -1, DefaultSprinkler(), DefaultYields())
		after := sc.InputsAt(161, 0, -1, DefaultSprinkler(), DefaultYields())
		if before != (Inputs{}) || after != (Inputs{}) {
			t.Fatalf("kind %v active outside its window: %+v %+v", sc.Kind, before, after)
		}
	}
}

func TestDustHasSmokeButNoHeatOrCO(t *testing.T) {
	sc := Scenario{Kind: KindDust, T0: 0, Duration: 600}
	in := sc.InputsAt(10, 0, -1, DefaultSprinkler(), DefaultYields())
	if in.Smoke <= 0 || in.Heat != 0 || in.CO != 0 {
		t.Fatalf("dust inputs wrong: %+v", in)
	}
}

func TestCookingMakesSmokeWithLittleCOAndHeat(t *testing.T) {
	traj := run(Scenario{Kind: KindCooking, T0: 0, Duration: 600}, 600, -1)
	end := traj[len(traj)-1]
	if end.K < 0.05 {
		t.Fatalf("cooking smoke too low to be a realistic false-alarm source: K=%v", end.K)
	}
	if end.C > 20 {
		t.Fatalf("cooking CO too high to be benign: C=%v", end.C)
	}
	if end.T-20 > 5 {
		t.Fatalf("cooking heats the room too much: dT=%v", end.T-20)
	}
}

func TestDustNeverHeatsOrAddsCO(t *testing.T) {
	traj := run(Scenario{Kind: KindDust, T0: 0, Duration: 600}, 600, -1)
	end := traj[len(traj)-1]
	if end.C != 0 || end.T-20 > 0.01 {
		t.Fatalf("dust changed CO or temperature: C=%v dT=%v", end.C, end.T-20)
	}
}

func TestMediumFireReachesDetectableLevelsInPlausibleTime(t *testing.T) {
	traj := run(Scenario{Kind: KindFire, T0: 0, Alpha: AlphaMedium, QMax: 1000}, 180, -1)
	if traj[60].K < 0.1 {
		t.Errorf("smoke K=%v at 60 s, want >= 0.1", traj[60].K)
	}
	if traj[90].C < 100 {
		t.Errorf("CO=%v ppm at 90 s, want >= 100", traj[90].C)
	}
	if traj[120].T-20 < 10 {
		t.Errorf("temperature rise %v at 120 s, want >= 10", traj[120].T-20)
	}
}

func TestDriftedSmokeCarriesSmokeAndCOButNoHeat(t *testing.T) {
	sc := Scenario{Kind: KindDrift, T0: 0, Alpha: AlphaMedium}
	in := sc.InputsAt(60, 0, -1, DefaultSprinkler(), DefaultYields())
	if in.Smoke <= 0 || in.CO <= 0 {
		t.Fatalf("drift must carry smoke and CO: %+v", in)
	}
	if in.Heat != 0 {
		t.Fatalf("drift must carry no heat: %+v", in)
	}
}

func TestDriftReachesFireLevelSmokeAndCOWithoutWarmingTheRoom(t *testing.T) {
	// what a neighbouring room sees when a fire next door fills it with smoke
	traj := run(Scenario{Kind: KindDrift, T0: 0, Alpha: AlphaMedium}, 400, -1)
	end := traj[len(traj)-1]
	if end.K < 0.15 || end.C < 100 {
		t.Fatalf("drift never became a fire-level smoke and CO signal: K=%v C=%v", end.K, end.C)
	}
	if end.T-20 > 1 {
		t.Fatalf("drift warmed the room by %v °C", end.T-20)
	}
}

func TestFireIsStillUndetectableAtIgnition(t *testing.T) {
	traj := run(Scenario{Kind: KindFire, T0: 0, Alpha: AlphaMedium, QMax: 1000}, 10, -1)
	if traj[10].K > 0.01 || traj[10].C > 5 {
		t.Fatalf("fire detectable too early: K=%v C=%v", traj[10].K, traj[10].C)
	}
}

func TestSprinklerBringsTheFireUnderControl(t *testing.T) {
	sc := Scenario{Kind: KindFire, T0: 0, Alpha: AlphaFast, QMax: 1000}
	traj := run(sc, 900, 120)
	peakT := 0.0
	for _, r := range traj {
		if r.T > peakT {
			peakT = r.T
		}
	}
	end := traj[len(traj)-1]
	if end.T > peakT-15 {
		t.Fatalf("sprinkler did not cool the room: peak %v end %v", peakT, end.T)
	}
	if end.T > 40 {
		t.Fatalf("room still hot after suppression: T=%v", end.T)
	}
}
