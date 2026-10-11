package detector

import (
	"math"
	"math/rand"
	"testing"
)

func TestDeployedWithoutAModelIsTheRuleBaseline(t *testing.T) {
	th := DefaultThresholds()
	classify := Deployed(th, nil)
	for _, v := range [][]float64{
		vec(0, 3, 21, 0, 0, 0), vec(0.5, 3, 21, 0, 0, 0), vec(0.3, 150, 21, 0, 0, 0),
		vec(0.5, 300, 60, 0.02, 4, 0.3), vec(math.NaN(), 3, 21, math.NaN(), 0, 0),
	} {
		if got, want := classify(v), th.Classify(v); got != want {
			t.Errorf("vector %v: deployed %v, rules %v", v, got, want)
		}
	}
}

func TestTheModelCanNeverLowerWhatTheRulesSay(t *testing.T) {
	th := DefaultThresholds()
	classify := Deployed(th, trainedModel(t))
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 3000; i++ {
		v := []float64{
			rng.Float64() * 2, rng.Float64() * 400, 15 + rng.Float64()*80,
			rng.NormFloat64() * 0.05, rng.NormFloat64() * 5, rng.NormFloat64() * 0.3,
		}
		if rng.Intn(4) == 0 { // some failed sensors
			j := rng.Intn(3)
			v[j], v[j+3] = math.NaN(), math.NaN()
		}
		if got, floor := classify(v), th.Classify(v); got < floor {
			t.Fatalf("vector %v: deployed %v is below the rule floor %v", v, got, floor)
		}
	}
}

func TestModelBlindSpotIsCoveredByTheRuleFloor(t *testing.T) {
	m := trainedModel(t)
	th := DefaultThresholds()
	// smoke 5 1/m and CO 500 ppm with flat readings and a normal temperature:
	// two features far outside the training range, four inside it
	v := vec(5, 500, 21, 0, 0, 0)
	if got := Deployed(th, m)(v); got < TierWarning {
		t.Fatalf("deployed = %v: a room full of smoke and lethal CO must never read as normal", got)
	}
}

func TestDeployedNeedsHeatBeforeFireOnTheModelPathToo(t *testing.T) {
	classify := Deployed(DefaultThresholds(), trainedModel(t))
	if got := classify(vec(5, 500, 21, 0, 0, 0)); got == TierFire {
		t.Fatal("smoke and CO without heat reached the fire tier")
	}
	if got := classify(vec(5, 500, 100, 0.1, 5, 1)); got != TierFire {
		t.Fatalf("a real fire (with heat) = %v, want fire", got)
	}
}

func TestDeployedKeepsTheSafetyFirstRulesWhenTheHeatSensorIsDown(t *testing.T) {
	classify := Deployed(DefaultThresholds(), trainedModel(t))
	got := classify(vec(5, 500, math.NaN(), 0.1, 5, math.NaN()))
	if got != TierFire {
		t.Fatalf("tier = %v, want fire: a dead heat sensor must not hide a fire", got)
	}
}
