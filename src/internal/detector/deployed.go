package detector

// Deployed returns the classifier the system actually runs, so the detector
// service and the evaluation use one definition and cannot drift apart.
//
// The rule baseline is a safety floor under the learned model (the Simplex
// pattern from the course reading): the model may raise the tier, for example
// by spotting an early joint deviation, but it can never lower what the rules
// say. Measured reason: an isolation forest dilutes an anomaly that sits in
// only two of its six features, and rated a room at smoke 5 1/m and CO 500 ppm
// with flat readings as normal. Both paths pass the heat gate, so smoke and CO
// alone arm a room but only heat discharges its sprinkler (found in the
// closed-loop run, where a neighbouring room was flooded on drifted smoke).
//
// A nil model gives the rule baseline alone, the fallback when no model is
// loaded.
func Deployed(th Thresholds, m *Model) func(vec []float64) Tier {
	return func(vec []float64) Tier {
		rules := th.Classify(vec)
		if m == nil {
			return rules
		}
		return max(rules, th.RequireHeat(m.Classify(vec), vec))
	}
}
