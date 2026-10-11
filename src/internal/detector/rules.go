package detector

import "math"

// Tier is the detector's response level (decision D3). The order matters:
// a higher tier always means a stronger response.
type Tier int

const (
	TierNone    Tier = iota // normal
	TierWarning             // pre-alert only (for example smoke alone), never discharges by itself
	TierArmed               // smoke and CO at fire level but no heat: pre-alert, discharges only on heat or after a long persistence
	TierFire                // full response
)

func (t Tier) String() string {
	switch t {
	case TierNone:
		return "none"
	case TierWarning:
		return "warning"
	case TierArmed:
		return "armed"
	case TierFire:
		return "fire"
	}
	return "unknown"
}

// Thresholds is the rule-based baseline detector. It is the comparison point
// for the learned model (D9) and the fallback when the model is unavailable.
// Values are starting points to tune against held-out scenarios.
type Thresholds struct {
	SmokeWarn, SmokeFire float64 // extinction coefficient [1/m]
	COWarn, COFire       float64 // [ppm]
	TempWarn, TempFire   float64 // absolute temperature [°C]
	RiseWarn, RiseFire   float64 // temperature rate of rise [K/s]
}

// DefaultThresholds follow common heat-detector practice (57 °C fixed
// temperature, about 8.3 K/min rate of rise for the fire level).
func DefaultThresholds() Thresholds {
	return Thresholds{
		SmokeWarn: 0.05, SmokeFire: 0.15,
		COWarn: 30, COFire: 100,
		TempWarn: 35, TempFire: 57,
		RiseWarn: 0.05, RiseFire: 0.139,
	}
}

// Classify turns a feature vector (FeatureNames order) into a tier. While all
// three signals work, one signal alone can only raise a warning, so a single
// false-alarm source such as dust (smoke only) or steam cannot trigger the
// full response; a fire needs two independent signals at their fire level.
//
// A missing signal is NaN. Then the number of confirmations needed shrinks to
// the number of signals left (at most two), because a fire plus one failed
// sensor must not be missed: safety first, then availability (D2). The price
// is more false alarms in degraded mode; with a single working signal, dust
// cannot be told from a fire. A missing signal always raises at least a
// warning, and a malformed vector is never read as normal.
func (th Thresholds) Classify(v []float64) Tier {
	if len(v) != len(FeatureNames) {
		return TierWarning
	}
	smoke, co, temp, dTemp := v[0], v[1], v[2], v[5]

	available := 0
	for _, x := range []float64{smoke, co, temp} {
		if !math.IsNaN(x) {
			available++
		}
	}
	if available == 0 {
		return TierWarning
	}
	missing := len(FeatureNames)/2 - available
	required := min(2, available)

	warn := 0
	fire := 0
	if smoke >= th.SmokeWarn {
		warn++
	}
	if smoke >= th.SmokeFire {
		fire++
	}
	if co >= th.COWarn {
		warn++
	}
	if co >= th.COFire {
		fire++
	}
	if temp >= th.TempWarn || dTemp >= th.RiseWarn {
		warn++
	}
	if temp >= th.TempFire || dTemp >= th.RiseFire {
		fire++
	}

	switch {
	case fire >= required:
		return th.RequireHeat(TierFire, v)
	case warn >= 1 || missing > 0:
		return TierWarning
	}
	return TierNone
}

// HasHeat reports whether the room itself is showing heat: the temperature at
// the warning level, or climbing at the warning rate. available is false when
// the temperature sensor is down and nothing can be said about heat.
func (th Thresholds) HasHeat(v []float64) (evidence, available bool) {
	if len(v) != len(FeatureNames) || math.IsNaN(v[2]) {
		return false, false
	}
	return v[2] >= th.TempWarn || v[5] >= th.RiseWarn, true
}

// RequireHeat is the pre-action rule: smoke and CO can arm a room (TierArmed)
// but only heat in that room discharges its sprinkler. Smoke and CO drift
// through open doors from a fire next door; heat does not, so without this a
// neighbouring room would be flooded on smoke alone (found in the closed-loop
// run). When the temperature sensor is down the gate steps aside, so a fire
// with a failed heat sensor is still caught (D2). It never raises a tier.
func (th Thresholds) RequireHeat(tier Tier, v []float64) Tier {
	if tier != TierFire {
		return tier
	}
	if len(v) != len(FeatureNames) {
		return TierWarning
	}
	if evidence, available := th.HasHeat(v); available && !evidence {
		return TierArmed
	}
	return TierFire
}
