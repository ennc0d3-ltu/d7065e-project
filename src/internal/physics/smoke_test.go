package physics

import (
	"math"
	"testing"
)

func TestSmokeSourceRaisesExtinction(t *testing.T) {
	r := Room{Volume: 100, T: 20}
	// dK = Smoke/V * dt = 5/100 = 0.05
	got := StepRoom(r, testParams(), Inputs{Smoke: 5}, 1)
	approx(t, got.K, 0.05, 1e-12)
}

func TestSmokeDecaysByVentilationAndSettling(t *testing.T) {
	r := Room{Volume: 100, T: 20, K: 1}
	// dK = -(0.001+0.0002)*1 = -0.0012
	got := StepRoom(r, testParams(), Inputs{}, 1)
	approx(t, got.K, 1-0.0012, 1e-12)
}

func TestCOSourceRaisesConcentration(t *testing.T) {
	r := Room{Volume: 100, T: 20}
	// dC = CO/V * dt = 200/100 = 2 ppm
	got := StepRoom(r, testParams(), Inputs{CO: 200}, 1)
	approx(t, got.C, 2, 1e-12)
}

func TestCODecaysByVentilationOnly(t *testing.T) {
	r := Room{Volume: 100, T: 20, C: 100}
	// dC = -0.001*100 = -0.1
	got := StepRoom(r, testParams(), Inputs{}, 1)
	approx(t, got.C, 99.9, 1e-12)
}

func TestSmokeAndCONeverGoNegative(t *testing.T) {
	r := Room{Volume: 100, T: 20, K: 1e-9, C: 1e-9}
	p := testParams()
	p.LamVent = 5 // large enough that one Euler step would overshoot below zero
	got := StepRoom(r, p, Inputs{}, 1)
	if got.K < 0 || got.C < 0 || math.IsNaN(got.K) || math.IsNaN(got.C) {
		t.Fatalf("negative or NaN concentration: K=%v C=%v", got.K, got.C)
	}
}
