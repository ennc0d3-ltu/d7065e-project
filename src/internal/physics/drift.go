package physics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ClockStatus is the latest reading of how far the occupancy simulator's clock
// has moved from ours.
type ClockStatus struct {
	DriftSec float64 `json:"drift_s"`         // occupancy minus ours, sim seconds; + means the people run ahead
	Exceeded bool    `json:"exceeded"`        // |drift| beyond the warning threshold
	Error    string  `json:"error,omitempty"` // why the last reading failed; DriftSec is then the last good one
}

// DriftMonitor measures clock drift between this simulator and the course
// occupancysim (decision D5: the two clocks run side by side and are aligned at
// start, not locked). The people feed our heat balance and the responder's
// head counts, so people drifting ahead of or behind the physics means the
// wrong crowd is in the room. The first successful reading sets the baseline;
// after that, drift is how much further apart the two clocks have moved.
//
// occupancysim's clock can only be set to a whole minute, so drift is detected
// and reported, not corrected.
type DriftMonitor struct {
	// Warn is the drift, in sim seconds, beyond which Status().Exceeded is set.
	Warn float64
	// Sim returns our current sim time; Poll uses it.
	Sim func() float64

	base   string
	factor float64
	client *http.Client

	mu     sync.Mutex
	set    bool
	occ0   time.Time
	sim0   float64
	status ClockStatus
}

// NewDriftMonitor reads the occupancy clock of the simulator at baseURL.
// factor is the sim seconds per real second both clocks should run at (kept
// for reference in logs).
func NewDriftMonitor(baseURL string, factor float64) *DriftMonitor {
	return &DriftMonitor{Warn: 10, base: baseURL, factor: factor, client: &http.Client{Timeout: 3 * time.Second}}
}

// Measure reads the occupancy clock and returns the drift against simNow.
func (m *DriftMonitor) Measure(ctx context.Context, simNow float64) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/api/state", nil)
	if err != nil {
		return 0, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("occupancy state: HTTP %d", resp.StatusCode)
	}
	var st struct {
		Sim struct {
			Clock struct {
				Time string `json:"time"`
			} `json:"clock"`
		} `json:"sim"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0, fmt.Errorf("occupancy state: %w", err)
	}
	if st.Sim.Clock.Time == "" {
		return 0, errors.New("occupancy state has no clock time")
	}
	occ, err := time.Parse(time.RFC3339, st.Sim.Clock.Time)
	if err != nil {
		return 0, fmt.Errorf("occupancy clock: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.set {
		m.set, m.occ0, m.sim0 = true, occ, simNow
		return 0, nil
	}
	return occ.Sub(m.occ0).Seconds() - (simNow - m.sim0), nil
}

// Poll takes one reading and updates Status.
func (m *DriftMonitor) Poll(ctx context.Context) {
	d, err := m.Measure(ctx, m.Sim())
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.status.Error = err.Error()
		return
	}
	m.status = ClockStatus{DriftSec: d, Exceeded: abs(d) > m.Warn}
}

// Status is the latest result.
func (m *DriftMonitor) Status() ClockStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Run polls every interval until ctx ends and calls onChange whenever the
// warning state flips, with the new status.
func (m *DriftMonitor) Run(ctx context.Context, every time.Duration, onChange func(ClockStatus)) {
	t := time.NewTicker(every)
	defer t.Stop()
	var was bool
	for {
		m.Poll(ctx)
		if st := m.Status(); st.Exceeded != was && onChange != nil {
			was = st.Exceeded
			onChange(st)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
