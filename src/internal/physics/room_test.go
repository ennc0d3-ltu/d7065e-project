package physics

import "testing"

// testParams are explicit round numbers so expected values are hand-checkable.
func testParams() Params {
	return Params{
		CTh: 1000, KLoss: 2, QOcc: 0.1, QSpr: 0.5, TWet: 15, TOut: 20,
		LamVent: 0.001, LamSet: 0.0002, Beta: 1,
	}
}

func TestTemperatureStaysAtAmbientWithNoSources(t *testing.T) {
	r := Room{Volume: 100, T: 20}
	got := StepRoom(r, testParams(), Inputs{}, 1)
	approx(t, got.T, 20, 1e-12)
}

func TestHeatReleaseWarmsTheRoom(t *testing.T) {
	r := Room{Volume: 100, T: 20}
	// dT = dt/CTh * (loss 0 + Q 100) = 0.1
	got := StepRoom(r, testParams(), Inputs{Heat: 100}, 1)
	approx(t, got.T, 20.1, 1e-9)
}

func TestEnvelopeLossCoolsTowardOutside(t *testing.T) {
	r := Room{Volume: 100, T: 30}
	// dT = 1/1000 * 2 * (20-30) = -0.02
	got := StepRoom(r, testParams(), Inputs{}, 1)
	approx(t, got.T, 29.98, 1e-9)
}

func TestOccupantsAddBodyHeat(t *testing.T) {
	r := Room{Volume: 100, T: 20}
	// 10 people * 0.1 kW = 1 kW -> dT = 0.001
	got := StepRoom(r, testParams(), Inputs{People: 10}, 1)
	approx(t, got.T, 20.001, 1e-9)
}

func TestSprinklerCoolsAWarmRoom(t *testing.T) {
	r := Room{Volume: 100, T: 50}
	// loss: 2*(20-50) = -60; sprinkler: -0.5*1*(50-15) = -17.5; dT = -77.5/1000
	got := StepRoom(r, testParams(), Inputs{Sprinkler: 1}, 1)
	approx(t, got.T, 50-0.0775, 1e-9)
}
