package physics

import (
	"math"
	"testing"
)

func approx(t *testing.T, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("got %v, want %v (±%v)", got, want, tol)
	}
}

func TestFireIsZeroBeforeIgnition(t *testing.T) {
	f := Fire{Alpha: AlphaMedium, T0: 100, QMax: 1000}
	approx(t, f.HRR(99), 0, 1e-9)
}

func TestFireGrowsAsTSquared(t *testing.T) {
	f := Fire{Alpha: AlphaMedium, T0: 100, QMax: 1000}
	// 10 s after ignition: 0.0117 * 10^2 = 1.17 kW
	approx(t, f.HRR(110), 1.17, 1e-9)
}

func TestFireIsCappedAtQMax(t *testing.T) {
	f := Fire{Alpha: AlphaFast, T0: 0, QMax: 1000}
	approx(t, f.HRR(10000), 1000, 1e-9)
}

func TestSuppressionDecaysFireOverTime(t *testing.T) {
	f := Fire{Alpha: AlphaFast, T0: 0, QMax: 1000}
	sp := Sprinkler{TauSup: 60}
	// Sprinkler fully on from t=200. At t=200 nothing has decayed yet.
	q0 := f.Effective(200, 1.0, 200, sp)
	approx(t, q0, f.HRR(200), 1e-9)
	// One time constant later the remaining fraction is exp(-1).
	q1 := f.Effective(260, 1.0, 200, sp)
	approx(t, q1, f.HRR(260)*math.Exp(-1), 1e-6)
}

func TestSprinklerOffLeavesFireUnchanged(t *testing.T) {
	f := Fire{Alpha: AlphaFast, T0: 0, QMax: 1000}
	sp := Sprinkler{TauSup: 60}
	approx(t, f.Effective(260, 0, 200, sp), f.HRR(260), 1e-9)
}
