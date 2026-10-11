// Package gateway turns ground truth into what a real sensor would report:
// delayed, noisy and sometimes broken (docs/L1/04-physical-model.md §6).
package gateway

import "math/rand"

// Fault is an injected sensor failure mode.
type Fault int

const (
	FaultNone    Fault = iota
	FaultStuck         // keeps reporting its last value
	FaultOffline       // reports nothing
	FaultDrift         // adds a bias that grows linearly with time
)

type sample struct{ t, v float64 }

// Degrader applies lag, Gaussian noise and an optional fault to one sensor's
// truth signal. It is not safe for concurrent use; the gateway owns it.
type Degrader struct {
	sigma, lag float64
	rng        *rand.Rand
	buf        []sample

	fault    Fault
	since    float64 // sim time the current fault began
	drift    float64 // drift rate [units/s]
	last     float64 // last value reported
	haveLast bool
	stuckVal float64
	stuckSet bool
}

// NewDegrader returns a degrader with noise std-dev sigma (sensor units), a
// lag in sim seconds and a seeded random source (reproducible evidence).
func NewDegrader(sigma, lag float64, seed int64) *Degrader {
	return &Degrader{sigma: sigma, lag: lag, rng: rand.New(rand.NewSource(seed))}
}

// SetDrift sets the drift rate used while the fault is FaultDrift.
func (d *Degrader) SetDrift(rate float64) { d.drift = rate }

// SetFault switches the failure mode at sim time t. FaultNone clears it.
func (d *Degrader) SetFault(f Fault, t float64) {
	d.fault, d.since = f, t
	d.stuckSet = false
	if f == FaultStuck && d.haveLast {
		d.stuckVal, d.stuckSet = d.last, true
	}
}

// Observe feeds the true value at sim time t and returns what the sensor
// reports. ok is false when the sensor is offline and sends nothing.
func (d *Degrader) Observe(t, truth float64) (v float64, ok bool) {
	d.buf = append(d.buf, sample{t, truth})
	lagged := d.laggedAt(t)

	switch d.fault {
	case FaultOffline:
		return 0, false
	case FaultStuck:
		if !d.stuckSet {
			d.stuckVal, d.stuckSet = lagged, true
		}
		return d.stuckVal, true
	}

	v = lagged + d.rng.NormFloat64()*d.sigma
	if d.fault == FaultDrift {
		v += d.drift * (t - d.since)
	}
	d.last, d.haveLast = v, true
	return v, true
}

// laggedAt returns the truth the sensor is physically showing at time t: the
// latest sample at or before t-lag, or the oldest one during warm-up. It
// drops samples that can no longer be selected.
func (d *Degrader) laggedAt(t float64) float64 {
	idx := 0
	for i, s := range d.buf {
		if s.t <= t-d.lag {
			idx = i
		}
	}
	v := d.buf[idx].v
	d.buf = d.buf[idx:]
	return v
}
