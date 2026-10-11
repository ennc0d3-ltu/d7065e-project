// Package physics is the ground-truth model of the building: fire source,
// heat, smoke and CO balances per room (docs/L1/04-physical-model.md).
package physics

import "math"

// Fire growth rates α [kW/s²] for t-squared fires.
const (
	AlphaSlow   = 0.0029
	AlphaMedium = 0.0117
	AlphaFast   = 0.0469
)

// Fire is a t-squared fire in one room. Times are sim seconds.
type Fire struct {
	Alpha float64 // growth rate [kW/s²]
	T0    float64 // ignition time [s]
	QMax  float64 // ventilation/fuel limit [kW]
}

// Sprinkler holds the suppression parameters.
type Sprinkler struct {
	TauSup float64 // suppression time constant [s]
}

// HRR is the unsuppressed heat-release rate at time t [kW].
func (f Fire) HRR(t float64) float64 {
	if t < f.T0 {
		return 0
	}
	dt := t - f.T0
	return math.Min(f.Alpha*dt*dt, f.QMax)
}

// Effective is the heat-release rate after sprinkler suppression [kW].
// s is the sprinkler state in [0,1]; ta is when it came on.
func (f Fire) Effective(t, s, ta float64, sp Sprinkler) float64 {
	q := f.HRR(t)
	if s <= 0 || t < ta {
		return q
	}
	return q * (1 - s*(1-math.Exp(-(t-ta)/sp.TauSup)))
}
