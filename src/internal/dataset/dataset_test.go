package dataset

import (
	"math"
	"reflect"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/physics"
)

func fireSpec(seed int64) RunSpec {
	return RunSpec{Name: "fire", Kind: KindFire, Alpha: physics.AlphaMedium, T0: 300, Secs: 900, Seed: seed}
}

func TestGenerationIsDeterministicForASeed(t *testing.T) {
	a, b := Generate(fireSpec(5)), Generate(fireSpec(5))
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different runs")
	}
	if reflect.DeepEqual(a, Generate(fireSpec(6))) {
		t.Fatal("different seeds produced identical runs")
	}
}

func TestOnePointPerSamplePeriod(t *testing.T) {
	run := Generate(RunSpec{Name: "n", Kind: KindNormal, Secs: 600, Seed: 1})
	if got, want := len(run.Points), 600/SamplePeriod; got != want {
		t.Fatalf("points = %d, want %d", got, want)
	}
	for _, p := range run.Points {
		if len(p.Vec) != len(detector.FeatureNames) {
			t.Fatalf("vector length %d at t=%v", len(p.Vec), p.T)
		}
	}
}

func TestNormalRunsStayQuiet(t *testing.T) {
	run := Generate(RunSpec{Name: "n", Kind: KindNormal, Secs: 1800, Seed: 2})
	for _, p := range run.Points {
		if p.Vec[0] > 0.05 || p.Vec[1] > 30 {
			t.Fatalf("normal run raised smoke/CO at t=%v: %v", p.T, p.Vec)
		}
		if p.Fire {
			t.Fatal("a normal run labelled a point as fire")
		}
	}
}

func TestFireRunIsLabelledFromIgnitionAndBecomesDetectable(t *testing.T) {
	run := Generate(fireSpec(3))
	var lastSmoke float64
	for _, p := range run.Points {
		if want := p.T >= 300; p.Fire != want {
			t.Fatalf("t=%v labelled fire=%v, want %v", p.T, p.Fire, want)
		}
		lastSmoke = p.Vec[0]
	}
	if lastSmoke < 0.15 {
		t.Fatalf("fire never produced detectable smoke: %v", lastSmoke)
	}
}

func TestOfflineSensorLeavesMissingFeaturesFromTheFailureTime(t *testing.T) {
	spec := fireSpec(8)
	spec.Offline = []string{"smoke"}
	spec.OfflineFrom = 200
	run := Generate(spec)
	for _, p := range run.Points {
		missing := math.IsNaN(p.Vec[0]) && math.IsNaN(p.Vec[3])
		// stale only after maxAge (15 s) beyond the failure time
		switch {
		case p.T <= 200 && missing:
			t.Fatalf("t=%v: smoke missing before the failure", p.T)
		case p.T > 220 && !missing:
			t.Fatalf("t=%v: smoke still present after the sensor died", p.T)
		}
		if math.IsNaN(p.Vec[1]) || math.IsNaN(p.Vec[2]) {
			t.Fatalf("t=%v: a healthy signal went missing", p.T)
		}
	}
}

func TestTrainingSetNeverContainsAFire(t *testing.T) {
	runs := TrainingRuns()
	if len(runs) == 0 {
		t.Fatal("no training runs")
	}
	kinds := map[Kind]int{}
	for _, r := range runs {
		kinds[r.Kind]++
		for _, p := range r.Points {
			if p.Fire {
				t.Fatalf("run %q contains a fire point at t=%v: the one-class model must never see a fire", r.Name, p.T)
			}
		}
	}
	if kinds[KindFire] != 0 {
		t.Fatalf("fire runs in the training set: %v", kinds)
	}
	for _, k := range []Kind{KindNormal, KindCooking, KindDust} {
		if kinds[k] == 0 {
			t.Fatalf("no %v runs in the training set: %v", k, kinds)
		}
	}
}

func TestTrainingSetIsDeterministic(t *testing.T) {
	a, b := TrainingRuns(), TrainingRuns()
	if len(a) != len(b) || !reflect.DeepEqual(a[0], b[0]) || !reflect.DeepEqual(a[len(a)-1], b[len(b)-1]) {
		t.Fatal("two calls produced different training sets")
	}
}

func TestBenignRunsAreNeverLabelledFire(t *testing.T) {
	for _, k := range []Kind{KindCooking, KindDust} {
		run := Generate(RunSpec{Name: "b", Kind: k, T0: 200, Duration: 400, Secs: 900, Seed: 4})
		raised := false
		for _, p := range run.Points {
			if p.Fire {
				t.Fatalf("%v labelled a point as fire", k)
			}
			if p.Vec[0] > 0.03 {
				raised = true
			}
		}
		if !raised {
			t.Fatalf("%v run produced no smoke at all", k)
		}
	}
}

func TestSmokeDriftRunHasSmokeAndCOWithoutHeatAndIsNotAFire(t *testing.T) {
	run := Generate(RunSpec{Name: "drift", Kind: KindDrift, Alpha: physics.AlphaMedium, T0: 200, Secs: 900, Seed: 9})
	last := run.Points[len(run.Points)-1].Vec
	if last[0] < 0.15 || last[1] < 100 {
		t.Fatalf("drift never reached fire-level smoke and CO: %v", last)
	}
	if last[2] > 23 {
		t.Fatalf("drift warmed the room: temp %v", last[2])
	}
	for _, p := range run.Points {
		if p.Fire {
			t.Fatal("a drift run is not a fire in this room and must never be labelled one")
		}
	}
	if KindDrift.String() != "drift" {
		t.Fatalf("name = %q", KindDrift.String())
	}
}

func TestSprinklerRunCoolsDownAfterSuppression(t *testing.T) {
	spec := fireSpec(7)
	spec.SprinklerAt = 420
	run := Generate(spec)
	peak, end := 0.0, run.Points[len(run.Points)-1].Vec[2]
	for _, p := range run.Points {
		if p.Vec[2] > peak {
			peak = p.Vec[2]
		}
	}
	if end > peak-10 {
		t.Fatalf("sprinkler run did not cool: peak %v end %v", peak, end)
	}
}
