package detector

import (
	"math"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
)

func approx(t *testing.T, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("got %v, want %v (±%v)", got, want, tol)
	}
}

func TestSlopeOfALinearRiseIsItsGradient(t *testing.T) {
	w := NewWindow(20) // 10 s of samples spans half the window
	for s := 0; s <= 10; s++ {
		w.Add(float64(s), 20+0.5*float64(s))
	}
	got, ok := w.Slope()
	if !ok {
		t.Fatal("slope unavailable")
	}
	approx(t, got, 0.5, 1e-9)
}

func TestSlopeNeedsEnoughSamples(t *testing.T) {
	w := NewWindow(60)
	w.Add(0, 1)
	w.Add(1, 2)
	if _, ok := w.Slope(); ok {
		t.Fatal("slope from two samples must not be trusted")
	}
}

func TestASlopeOverAShortSpanIsNotTrusted(t *testing.T) {
	// startup: five noisy samples over four seconds fake a steep rise
	w := NewWindow(60)
	for i, v := range []float64{20.0, 20.4, 19.8, 20.5, 20.9} {
		w.Add(float64(i), v)
	}
	if _, ok := w.Slope(); ok {
		t.Fatal("a slope over 4 s of a 60 s window was trusted: sensor noise reads as a rapid rise")
	}
}

func TestASlopeIsTrustedOnceItSpansHalfTheWindow(t *testing.T) {
	w := NewWindow(60)
	for s := 0; s <= 30; s++ {
		w.Add(float64(s), 20+0.1*float64(s))
	}
	got, ok := w.Slope()
	if !ok {
		t.Fatal("a slope over 30 s was not trusted")
	}
	approx(t, got, 0.1, 1e-9)
}

func TestOldSamplesFallOutOfTheHorizon(t *testing.T) {
	w := NewWindow(10)
	for s := 0; s <= 30; s++ { // steep rise, then a flat tail
		v := float64(s)
		if s > 20 {
			v = 20
		}
		w.Add(float64(s), v)
	}
	got, _ := w.Slope()
	approx(t, got, 0, 1e-9) // only the flat last 10 s remain
}

func TestOutOfOrderAndDuplicateSamplesAreIgnored(t *testing.T) {
	w := NewWindow(60)
	w.Add(5, 10)
	w.Add(5, 99) // duplicate time
	w.Add(3, 77) // older than the newest
	v, tm, ok := w.Latest()
	if !ok || v != 10 || tm != 5 {
		t.Fatalf("latest = %v@%v ok=%v, want 10@5", v, tm, ok)
	}
}

func reading(typ string, v, simTime float64) gateway.Reading {
	return gateway.Reading{Type: typ, Value: v, SimTime: simTime}
}

func feed(rf *RoomFeatures, until int) {
	for s := 0; s <= until; s++ {
		t := float64(s)
		rf.Add(reading("smoke", 0.01+0.002*t, t))
		rf.Add(reading("co", 5+1.0*t, t))
		rf.Add(reading("temperature", 20+0.1*t, t))
	}
}

func TestVectorHasLatestValuesAndSlopesInFixedOrder(t *testing.T) {
	rf := NewRoomFeatures(40, 15) // 20 s of samples spans half the window
	feed(rf, 20)
	vec, stale := rf.Vector(20)
	if len(stale) != 0 {
		t.Fatalf("stale = %v", stale)
	}
	if len(vec) != len(FeatureNames) {
		t.Fatalf("vector length %d, want %d", len(vec), len(FeatureNames))
	}
	want := map[string]float64{
		"smoke": 0.01 + 0.002*20, "co": 25, "temp": 22,
		"d_smoke": 0.002, "d_co": 1, "d_temp": 0.1,
	}
	for i, name := range FeatureNames {
		approx(t, vec[i], want[name], 1e-9)
	}
}

func TestASignalOlderThanMaxAgeIsReportedStale(t *testing.T) {
	rf := NewRoomFeatures(60, 15)
	feed(rf, 20)
	// smoke stops arriving; the others keep going to t=50
	for s := 21; s <= 50; s++ {
		rf.Add(reading("co", 30, float64(s)))
		rf.Add(reading("temperature", 23, float64(s)))
	}
	_, stale := rf.Vector(50)
	if len(stale) != 1 || stale[0] != "smoke" {
		t.Fatalf("stale = %v, want [smoke]", stale)
	}
}

func TestASignalNeverSeenIsStale(t *testing.T) {
	rf := NewRoomFeatures(60, 15)
	rf.Add(reading("co", 5, 1))
	_, stale := rf.Vector(2)
	if len(stale) != 2 {
		t.Fatalf("stale = %v, want smoke and temperature", stale)
	}
}

func TestDegradedVectorMarksOnlyTheStaleSignalAsMissing(t *testing.T) {
	rf := NewRoomFeatures(60, 15)
	feed(rf, 20)
	for s := 21; s <= 50; s++ { // smoke goes silent
		rf.Add(reading("co", 30, float64(s)))
		rf.Add(reading("temperature", 23, float64(s)))
	}
	vec, stale := rf.VectorDegraded(50)
	if len(stale) != 1 || stale[0] != "smoke" {
		t.Fatalf("stale = %v", stale)
	}
	for i, name := range FeatureNames {
		missing := name == "smoke" || name == "d_smoke"
		if math.IsNaN(vec[i]) != missing {
			t.Errorf("%s: NaN=%v, want %v", name, math.IsNaN(vec[i]), missing)
		}
	}
}

func TestDegradedVectorIsNilWhenEverySignalIsStale(t *testing.T) {
	rf := NewRoomFeatures(60, 15)
	vec, stale := rf.VectorDegraded(5)
	if vec != nil || len(stale) != 3 {
		t.Fatalf("vec=%v stale=%v", vec, stale)
	}
}

func TestDegradedVectorEqualsTheNormalVectorWhenNothingIsStale(t *testing.T) {
	rf := NewRoomFeatures(60, 15)
	feed(rf, 20)
	a, _ := rf.Vector(20)
	b, stale := rf.VectorDegraded(20)
	if len(stale) != 0 || len(a) != len(b) {
		t.Fatalf("stale=%v", stale)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("feature %d differs: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestUnknownReadingTypesAreIgnored(t *testing.T) {
	rf := NewRoomFeatures(60, 15)
	rf.Add(reading("humidity", 40, 1)) // must not panic or create a signal
	_, stale := rf.Vector(2)
	if len(stale) != 3 {
		t.Fatalf("stale = %v", stale)
	}
}
