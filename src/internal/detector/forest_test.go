package detector

import (
	"encoding/json"
	"math/rand"
	"testing"
)

// cluster returns n points around the origin with the given spread.
func cluster(n int, spread float64, seed int64) [][]float64 {
	rng := rand.New(rand.NewSource(seed))
	out := make([][]float64, n)
	for i := range out {
		out[i] = []float64{rng.NormFloat64() * spread, rng.NormFloat64() * spread}
	}
	return out
}

func mustTrain(t *testing.T, data [][]float64, trees, psi int, seed int64) *Forest {
	t.Helper()
	f, err := Train(data, trees, psi, seed)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func mustScore(t *testing.T, f *Forest, x []float64) float64 {
	t.Helper()
	s, err := f.Score(x)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOutliersScoreHigherThanInliers(t *testing.T) {
	f := mustTrain(t, cluster(500, 1, 1), 100, 256, 7)
	inlier := mustScore(t, f, []float64{0, 0})
	outlier := mustScore(t, f, []float64{8, 8})
	if outlier < inlier+0.15 {
		t.Fatalf("outlier %.3f is not clearly above inlier %.3f", outlier, inlier)
	}
	if outlier < 0.6 || inlier > 0.5 {
		t.Fatalf("scores out of the expected ranges: outlier %.3f inlier %.3f", outlier, inlier)
	}
}

func TestScoresStayInsideTheUnitInterval(t *testing.T) {
	f := mustTrain(t, cluster(300, 1, 2), 50, 128, 3)
	for _, x := range [][]float64{{0, 0}, {1, -1}, {100, 100}, {-50, 20}} {
		s := mustScore(t, f, x)
		if s <= 0 || s >= 1 {
			t.Fatalf("score(%v) = %v, want within (0,1)", x, s)
		}
	}
}

func TestTrainingIsDeterministicForAGivenSeed(t *testing.T) {
	data := cluster(300, 1, 4)
	a := mustTrain(t, data, 50, 128, 11)
	b := mustTrain(t, data, 50, 128, 11)
	for _, x := range [][]float64{{0, 0}, {3, 3}, {-6, 2}} {
		if mustScore(t, a, x) != mustScore(t, b, x) {
			t.Fatalf("same seed gave different scores at %v", x)
		}
	}
}

func TestForestSurvivesAJSONRoundTrip(t *testing.T) {
	f := mustTrain(t, cluster(300, 1, 5), 50, 128, 13)
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var back Forest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, x := range [][]float64{{0, 0}, {4, -4}, {9, 9}} {
		if mustScore(t, f, x) != mustScore(t, &back, x) {
			t.Fatalf("score changed after a JSON round trip at %v", x)
		}
	}
}

func TestTrainRejectsBadInput(t *testing.T) {
	if _, err := Train(nil, 10, 64, 1); err == nil {
		t.Error("empty data accepted")
	}
	if _, err := Train([][]float64{{1, 2}, {3}}, 10, 64, 1); err == nil {
		t.Error("ragged data accepted")
	}
	if _, err := Train(cluster(10, 1, 1), 0, 64, 1); err == nil {
		t.Error("zero trees accepted")
	}
}

func TestScoreRejectsAWrongSizedVector(t *testing.T) {
	f := mustTrain(t, cluster(100, 1, 6), 20, 64, 1)
	if _, err := f.Score([]float64{1, 2, 3}); err == nil {
		t.Fatal("wrong dimension accepted")
	}
}

func TestConstantFeaturesDoNotHangOrCrash(t *testing.T) {
	data := make([][]float64, 50)
	for i := range data {
		data[i] = []float64{1, 1}
	}
	f := mustTrain(t, data, 10, 32, 1)
	mustScore(t, f, []float64{1, 1})
	mustScore(t, f, []float64{5, 5})
}

func TestTinyTrainingSetStillWorks(t *testing.T) {
	f := mustTrain(t, [][]float64{{0, 0}}, 5, 256, 1)
	mustScore(t, f, []float64{0, 0})
}
