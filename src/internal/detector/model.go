package detector

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
)

// ScoreThresholds are the anomaly-score levels for the warning and fire tiers.
type ScoreThresholds struct {
	Warn float64 `json:"warn"`
	Fire float64 `json:"fire"`
}

// modelJSON is the stored form of a trained model.
type modelJSON struct {
	Forest      *Forest           `json:"forest"`
	Baseline    []float64         `json:"baseline"`
	Calibration []ScoreThresholds `json:"calibration"`
}

// MarshalJSON stores the forest, the baseline and every calibration, so the
// detector can load a model without the training data.
func (m *Model) MarshalJSON() ([]byte, error) {
	return json.Marshal(modelJSON{Forest: m.forest, Baseline: m.baseline, Calibration: m.cal[:]})
}

// UnmarshalJSON loads a stored model and rejects one that cannot classify.
func (m *Model) UnmarshalJSON(data []byte) error {
	var j modelJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	switch {
	case j.Forest == nil || len(j.Forest.Trees) == 0:
		return errors.New("detector: stored model has no forest")
	case len(j.Baseline) != j.Forest.Dim:
		return errors.New("detector: stored baseline does not match the forest dimension")
	case len(j.Calibration) != allSignalsMissing:
		return errors.New("detector: stored model lacks a calibration for every failure mode")
	}
	m.forest, m.baseline = j.Forest, j.Baseline
	copy(m.cal[:], j.Calibration)
	return nil
}

const allSignalsMissing = 1<<3 - 1 // bit i set means signal i is stale

// Model is the learned detector: an isolation forest, the training baseline
// used to stand in for a failed sensor, and one threshold pair per failure
// mode.
//
// Why per-failure-mode thresholds: an isolation forest's score saturates once
// a point is outside the training range, and the ceiling depends on how many
// features still deviate. With a sensor replaced by its baseline, fewer
// features deviate, so the ceiling is lower. Thresholds calibrated on the
// full signal set then sit above that ceiling and the fire never alerts
// (measured: recall 0.00 with the CO sensor down). Calibrating each mask on
// the training data scored under the same masking removes the problem.
type Model struct {
	forest   *Forest
	baseline []float64
	cal      [allSignalsMissing]ScoreThresholds // index = failure mask 0..6
}

// NewModel calibrates thresholds for every failure mode from the training
// data alone: warning at the 99th percentile of training scores, fire just
// above the highest. No fire data is used (proposal §4).
func NewModel(f *Forest, train [][]float64) *Model {
	m := &Model{forest: f, baseline: Baseline(train)}
	for mask := range m.cal {
		scores := make([]float64, 0, len(train))
		for _, row := range train {
			if s, err := f.Score(m.mask(row, mask)); err == nil {
				scores = append(scores, s)
			}
		}
		if len(scores) == 0 {
			m.cal[mask] = ScoreThresholds{Warn: 0.5, Fire: 0.6}
			continue
		}
		slices.Sort(scores)
		warn := scores[min(len(scores)-1, int(0.99*float64(len(scores))))]
		fire := scores[len(scores)-1] + 0.02
		m.cal[mask] = ScoreThresholds{Warn: warn, Fire: max(fire, warn+0.02)}
	}
	return m
}

// Thresholds returns the calibrated thresholds for a failure mask.
func (m *Model) Thresholds(mask int) ScoreThresholds { return m.cal[mask] }

// mask replaces the features of the signals in mask by their baseline.
func (m *Model) mask(vec []float64, mask int) []float64 {
	if mask == 0 {
		return vec
	}
	x := append([]float64(nil), vec...)
	for i := range signals {
		if mask&(1<<i) != 0 {
			x[i], x[i+len(signals)] = m.baseline[i], m.baseline[i+len(signals)]
		}
	}
	return x
}

// missingMask reads which signals are missing (NaN value) in vec.
func missingMask(vec []float64) int {
	mask := 0
	for i := range signals {
		if math.IsNaN(vec[i]) {
			mask |= 1 << i
		}
	}
	return mask
}

// Classify scores vec and returns a tier. A failed sensor (NaN features) is
// scored against its own calibration and can never lower the result below a
// warning: missing data is not "normal" (H7, NFR12). A malformed vector or a
// scoring error is never normal either.
func (m *Model) Classify(vec []float64) Tier {
	if len(vec) != len(FeatureNames) {
		return TierWarning
	}
	mask := missingMask(vec)
	if mask == allSignalsMissing {
		return TierWarning
	}
	score, err := m.forest.Score(m.mask(vec, mask))
	if err != nil {
		return TierWarning
	}
	th := m.cal[mask]
	tier := TierNone
	switch {
	case score >= th.Fire:
		tier = TierFire
	case score >= th.Warn:
		tier = TierWarning
	}
	if mask != 0 {
		tier = max(tier, TierWarning)
	}
	return tier
}
