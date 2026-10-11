// Package detector turns degraded sensor readings into a fire/no-fire
// decision: rolling features, an anomaly model, a rule baseline, and the
// escalation logic (decisions D3, D4, D9).
package detector

import (
	"math"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
)

// FeatureNames is the fixed order of the feature vector.
var FeatureNames = []string{"smoke", "co", "temp", "d_smoke", "d_co", "d_temp"}

// signals are the reading types the detector fuses, in vector order.
var signals = []string{"smoke", "co", "temperature"}

type sample struct{ t, v float64 }

// Window keeps one signal's samples over a sim-time horizon.
type Window struct {
	horizon float64
	buf     []sample
}

// NewWindow returns a window that keeps horizon sim seconds of samples.
func NewWindow(horizon float64) *Window { return &Window{horizon: horizon} }

// Add appends a sample. Samples not newer than the latest are ignored, which
// drops duplicates and out-of-order delivery (NFR12).
func (w *Window) Add(t, v float64) {
	if n := len(w.buf); n > 0 && t <= w.buf[n-1].t {
		return
	}
	w.buf = append(w.buf, sample{t, v})
	cut := 0
	for cut < len(w.buf) && w.buf[cut].t < t-w.horizon {
		cut++
	}
	w.buf = w.buf[cut:]
}

// Latest returns the newest value and its sim time.
func (w *Window) Latest() (v, t float64, ok bool) {
	if len(w.buf) == 0 {
		return 0, 0, false
	}
	s := w.buf[len(w.buf)-1]
	return s.v, s.t, true
}

// Slope is the least-squares gradient over the window [units/s]. It is
// trusted only once the samples span at least half the horizon: over a few
// seconds, sensor noise alone fakes a steep rise (found at start-up in the
// containerised run, where readings arrive every second).
func (w *Window) Slope() (float64, bool) {
	n := float64(len(w.buf))
	if n < 3 || w.buf[len(w.buf)-1].t-w.buf[0].t < w.horizon/2 {
		return 0, false
	}
	var st, sv float64
	for _, s := range w.buf {
		st += s.t
		sv += s.v
	}
	mt, mv := st/n, sv/n
	var num, den float64
	for _, s := range w.buf {
		num += (s.t - mt) * (s.v - mv)
		den += (s.t - mt) * (s.t - mt)
	}
	if den == 0 {
		return 0, false
	}
	return num / den, true
}

// RoomFeatures builds one room's feature vector from its readings.
type RoomFeatures struct {
	maxAge  float64
	windows map[string]*Window
}

// NewRoomFeatures keeps horizon seconds of history and treats a signal whose
// newest reading is older than maxAge as stale.
func NewRoomFeatures(horizon, maxAge float64) *RoomFeatures {
	rf := &RoomFeatures{maxAge: maxAge, windows: map[string]*Window{}}
	for _, s := range signals {
		rf.windows[s] = NewWindow(horizon)
	}
	return rf
}

// Add records a reading. Types the detector does not fuse are ignored.
func (rf *RoomFeatures) Add(r gateway.Reading) {
	if w, ok := rf.windows[r.Type]; ok {
		w.Add(r.SimTime, r.Value)
	}
}

// Vector returns the features at sim time now, in FeatureNames order. If any
// signal is missing or stale it returns no vector and the stale signal names:
// missing data must be reported as a warning, never treated as "normal" (H7).
func (rf *RoomFeatures) Vector(now float64) (vec []float64, stale []string) {
	vec, stale = rf.VectorDegraded(now)
	if len(stale) > 0 {
		return nil, stale
	}
	return vec, nil
}

// VectorDegraded is Vector for a detector that must keep working when a
// sensor fails: a stale signal's value and slope are NaN instead of the whole
// vector being withheld, and the stale names are still returned so the caller
// can raise the data-quality warning. It returns no vector only when every
// signal is stale.
func (rf *RoomFeatures) VectorDegraded(now float64) (vec []float64, stale []string) {
	n := len(signals)
	vals := make([]float64, n)
	slopes := make([]float64, n)
	for i, name := range signals {
		w := rf.windows[name]
		v, t, ok := w.Latest()
		if !ok || now-t > rf.maxAge {
			stale = append(stale, name)
			vals[i], slopes[i] = math.NaN(), math.NaN()
			continue
		}
		vals[i] = v
		slopes[i], _ = w.Slope() // flat until three samples exist
	}
	if len(stale) == n {
		return nil, stale
	}
	return append(vals, slopes...), stale
}
