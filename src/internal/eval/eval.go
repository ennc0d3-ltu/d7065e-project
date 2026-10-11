// Package eval measures a detector on generated runs (decision D9): recall on
// fires, false-alarm rate on benign runs, and detection delay. The same
// harness scores the rule baseline and the learned model, on the same runs.
package eval

import (
	"slices"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/dataset"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
)

// Classifier maps a feature vector to a tier, with no memory between calls.
type Classifier func(vec []float64) detector.Tier

// RulesClassifier is the threshold baseline.
func RulesClassifier(th detector.Thresholds) Classifier { return th.Classify }

// ModelClassifier scores with the learned model, including its per-failure-
// mode calibration (see detector.Model).
func ModelClassifier(m *detector.Model) Classifier { return m.Classify }

// RunResult is the outcome of one run.
type RunResult struct {
	Name       string
	Kind       dataset.Kind
	Detected   bool    // a fire run alarmed at or after ignition
	DelaySec   float64 // alarm time minus ignition (fire runs, when detected)
	FalseAlarm bool    // an alarm with no fire burning
	// Warned: a tier of at least warning held for the persistence window at
	// some point. For smoke drifting in from a fire next door this is the
	// "armed" state: the room is not blind, it simply has no heat to discharge on.
	Warned bool
	// WarnDelaySec is how long after ignition the warning held (fire runs
	// only). WarnedEarly marks a warning that held before ignition: a
	// nuisance, not a lead time, so it is not credited.
	WarnDelaySec float64
	WarnedEarly  bool
}

// EvaluateRun scans a run for a fire alarm: the fire tier held for `persist`
// consecutive samples. An alarm before ignition, or any alarm on a benign
// run, is a false alarm; warnings alone are not alarms. It also records
// whether a warning (or worse) held for the same window.
func EvaluateRun(run dataset.Run, c Classifier, persist int) RunResult {
	res := RunResult{Name: run.Name, Kind: run.Kind}
	fireStreak, warnStreak := 0, 0
	for _, p := range run.Points {
		tier := c(p.Vec)
		if tier == detector.TierFire {
			fireStreak++
		} else {
			fireStreak = 0
		}
		if tier >= detector.TierWarning {
			warnStreak++
		} else {
			warnStreak = 0
		}
		if warnStreak >= persist && !res.Warned {
			res.Warned = true
			if run.Kind == dataset.KindFire {
				if p.T < run.T0 {
					res.WarnedEarly = true
				} else {
					res.WarnDelaySec = p.T - run.T0
				}
			}
		}
		if fireStreak < persist {
			continue
		}
		switch {
		case run.Kind == dataset.KindFire && p.T >= run.T0:
			res.Detected, res.DelaySec = true, p.T-run.T0
		default:
			res.FalseAlarm = true
		}
		return res
	}
	return res
}

// Summary aggregates results over many runs.
type Summary struct {
	Fires, Benign  int
	Recall         float64 // detected fires / fire runs
	FalseAlarmRate float64 // benign runs with a false alarm / benign runs
	MedianDelaySec float64 // over detected fires
	Drift          int     // runs of smoke drifting in from a fire elsewhere
	ArmedRate      float64 // drift runs that reached a warning / drift runs

	MedianWarnDelaySec float64 // warning lead time on fires, ignition to a held warning
	NuisanceWarnRate   float64 // quiet (normal) runs that held a warning / normal runs
}

// Summarize computes recall, false-alarm rate and the median detection delay.
func Summarize(results []RunResult) Summary {
	var s Summary
	var delays, warnDelays []float64
	detected, falseAlarms, armed, normal, nuisance := 0, 0, 0, 0, 0
	for _, r := range results {
		if r.Kind == dataset.KindNormal {
			normal++
			if r.Warned {
				nuisance++
			}
		}
		if r.Kind == dataset.KindFire && r.Warned && !r.WarnedEarly {
			warnDelays = append(warnDelays, r.WarnDelaySec)
		}
		if r.Kind == dataset.KindDrift {
			s.Drift++
			if r.Warned {
				armed++
			}
		}
		if r.Kind == dataset.KindFire {
			s.Fires++
			if r.Detected {
				detected++
				delays = append(delays, r.DelaySec)
			}
			continue
		}
		s.Benign++
		if r.FalseAlarm {
			falseAlarms++
		}
	}
	if s.Fires > 0 {
		s.Recall = float64(detected) / float64(s.Fires)
	}
	if s.Benign > 0 {
		s.FalseAlarmRate = float64(falseAlarms) / float64(s.Benign)
	}
	if s.Drift > 0 {
		s.ArmedRate = float64(armed) / float64(s.Drift)
	}
	if normal > 0 {
		s.NuisanceWarnRate = float64(nuisance) / float64(normal)
	}
	s.MedianDelaySec = median(delays)
	s.MedianWarnDelaySec = median(warnDelays)
	return s
}

func median(xs []float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	slices.Sort(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}
