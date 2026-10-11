package gateway

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

func TestNoNoiseNoLagPassesTruthThrough(t *testing.T) {
	d := NewDegrader(0, 0, 1)
	v, ok := d.Observe(5, 21.5)
	if !ok {
		t.Fatal("reading dropped")
	}
	approx(t, v, 21.5, 1e-12)
}

func TestLagDelaysTheTruth(t *testing.T) {
	d := NewDegrader(0, 30, 1)
	var v float64
	for s := 0; s <= 100; s++ {
		v, _ = d.Observe(float64(s), float64(s)) // truth(t) = t
	}
	approx(t, v, 70, 1e-9) // at t=100 the sensor shows t-30
}

func TestLagWarmupShowsTheOldestSampleNotZero(t *testing.T) {
	d := NewDegrader(0, 30, 1)
	v, _ := d.Observe(0, 42)
	approx(t, v, 42, 1e-12)
	v, _ = d.Observe(5, 50)
	approx(t, v, 42, 1e-12) // still inside the lag window
}

func TestStuckSensorFreezesAtLastOutput(t *testing.T) {
	d := NewDegrader(0, 0, 1)
	for s := 0; s < 10; s++ {
		d.Observe(float64(s), float64(s))
	}
	d.SetFault(FaultStuck, 10)
	for s := 10; s < 20; s++ {
		v, ok := d.Observe(float64(s), 1000)
		if !ok {
			t.Fatal("a stuck sensor still reports")
		}
		approx(t, v, 9, 1e-12)
	}
}

func TestOfflineSensorReportsNothing(t *testing.T) {
	d := NewDegrader(0, 0, 1)
	d.SetFault(FaultOffline, 0)
	if _, ok := d.Observe(1, 20); ok {
		t.Fatal("offline sensor produced a reading")
	}
}

func TestDriftAddsALinearBias(t *testing.T) {
	d := NewDegrader(0, 0, 1)
	d.SetDrift(0.5) // units per second
	d.SetFault(FaultDrift, 100)
	v, _ := d.Observe(100, 20)
	approx(t, v, 20, 1e-12)
	v, _ = d.Observe(110, 20)
	approx(t, v, 25, 1e-12)
}

func TestClearingAFaultRestoresTruth(t *testing.T) {
	d := NewDegrader(0, 0, 1)
	d.SetFault(FaultOffline, 0)
	d.SetFault(FaultNone, 5)
	v, ok := d.Observe(6, 33)
	if !ok {
		t.Fatal("still offline after clearing")
	}
	approx(t, v, 33, 1e-12)
}

func TestNoiseIsZeroMeanWithTheRequestedSpread(t *testing.T) {
	d := NewDegrader(2, 0, 7)
	const n = 20000
	var sum, sumSq float64
	for i := 0; i < n; i++ {
		v, _ := d.Observe(float64(i), 10)
		sum += v
		sumSq += v * v
	}
	mean := sum / n
	std := math.Sqrt(sumSq/n - mean*mean)
	approx(t, mean, 10, 0.1)
	approx(t, std, 2, 0.1)
}

func TestSameSeedGivesTheSameReadings(t *testing.T) {
	a, b := NewDegrader(1, 0, 99), NewDegrader(1, 0, 99)
	for i := 0; i < 50; i++ {
		va, _ := a.Observe(float64(i), 5)
		vb, _ := b.Observe(float64(i), 5)
		if va != vb {
			t.Fatalf("diverged at %d: %v vs %v", i, va, vb)
		}
	}
}
