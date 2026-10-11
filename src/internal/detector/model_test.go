package detector

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"
)

// normalTraining is a tight cluster of quiet-room vectors in FeatureNames order.
func normalTraining(n int, seed int64) [][]float64 {
	rng := rand.New(rand.NewSource(seed))
	out := make([][]float64, n)
	for i := range out {
		out[i] = []float64{
			0.01 + rng.NormFloat64()*0.005, 3 + rng.NormFloat64(), 21 + rng.NormFloat64()*0.3,
			rng.NormFloat64() * 1e-4, rng.NormFloat64() * 0.01, rng.NormFloat64() * 0.005,
		}
	}
	return out
}

func trainedModel(t *testing.T) *Model {
	t.Helper()
	train := normalTraining(600, 1)
	f, err := Train(train, 100, 256, 7)
	if err != nil {
		t.Fatal(err)
	}
	return NewModel(f, train)
}

var extremeFire = []float64{5, 500, 100, 0.1, 5, 1}

func TestQuietRoomIsTierNone(t *testing.T) {
	m := trainedModel(t)
	if got := m.Classify([]float64{0.01, 3, 21, 0, 0, 0}); got != TierNone {
		t.Fatalf("tier = %v, want none", got)
	}
}

func TestExtremeFireIsTierFire(t *testing.T) {
	if got := trainedModel(t).Classify(extremeFire); got != TierFire {
		t.Fatalf("tier = %v, want fire", got)
	}
}

func TestFireIsStillTierFireWithAnyOneSignalMissing(t *testing.T) {
	m := trainedModel(t)
	nan := math.NaN()
	cases := map[string][]float64{
		"smoke missing": {nan, 500, 100, nan, 5, 1},
		"co missing":    {5, nan, 100, 0.1, nan, 1},
		"temp missing":  {5, 500, nan, 0.1, 5, nan},
	}
	for name, v := range cases {
		if got := m.Classify(v); got != TierFire {
			t.Errorf("%s: tier = %v, want fire (thresholds must be calibrated for this failure mode)", name, got)
		}
	}
}

func TestMissingSignalOnAQuietRoomIsAWarningNotNormal(t *testing.T) {
	m := trainedModel(t)
	nan := math.NaN()
	if got := m.Classify([]float64{0.01, nan, 21, 0, nan, 0}); got != TierWarning {
		t.Fatalf("tier = %v, want warning", got)
	}
}

func TestModelWithEverySignalMissingIsAWarning(t *testing.T) {
	nan := math.NaN()
	got := trainedModel(t).Classify([]float64{nan, nan, nan, nan, nan, nan})
	if got != TierWarning {
		t.Fatalf("tier = %v, want warning", got)
	}
}

func TestMalformedVectorIsAWarning(t *testing.T) {
	if got := trainedModel(t).Classify([]float64{1, 2}); got != TierWarning {
		t.Fatalf("tier = %v, want warning", got)
	}
}

func TestEachFailureModeHasItsOwnThresholds(t *testing.T) {
	m := trainedModel(t)
	full := m.Thresholds(0)
	noCO := m.Thresholds(1 << 1)
	if full == noCO {
		t.Fatalf("thresholds are identical for different failure modes: %+v", full)
	}
	for _, th := range []ScoreThresholds{full, noCO} {
		if th.Warn <= 0 || th.Fire <= th.Warn {
			t.Fatalf("unusable thresholds: %+v", th)
		}
	}
}

func TestModelSurvivesAJSONRoundTrip(t *testing.T) {
	m := trainedModel(t)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back Model
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	nan := math.NaN()
	for _, v := range [][]float64{
		{0.01, 3, 21, 0, 0, 0}, extremeFire,
		{nan, 500, 100, nan, 5, 1}, {5, nan, 100, 0.1, nan, 1}, {0.01, nan, 21, 0, nan, 0},
	} {
		if m.Classify(v) != back.Classify(v) {
			t.Fatalf("classification changed after a round trip for %v", v)
		}
	}
	for mask := 0; mask < allSignalsMissing; mask++ {
		if m.Thresholds(mask) != back.Thresholds(mask) {
			t.Fatalf("thresholds for mask %d changed", mask)
		}
	}
}

func TestLoadingAnEmptyModelFailsClearly(t *testing.T) {
	var m Model
	if err := json.Unmarshal([]byte(`{}`), &m); err == nil {
		t.Fatal("an empty model was accepted")
	}
}

func TestBaselineIsThePerFeatureMedian(t *testing.T) {
	got := Baseline([][]float64{{1, 10}, {3, 30}, {2, 20}})
	if got[0] != 2 || got[1] != 20 {
		t.Fatalf("baseline = %v, want [2 20]", got)
	}
}
