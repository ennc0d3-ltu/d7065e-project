package physics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeOccupancy answers GET /api/state with a settable clock.
type fakeOccupancy struct {
	srv *httptest.Server
	mu  sync.Mutex
	now time.Time
	bad bool
}

func newFakeOccupancy(t *testing.T, start time.Time) *fakeOccupancy {
	f := &fakeOccupancy{now: start}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.bad {
			_, _ = w.Write([]byte(`not json`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sim": map[string]any{"clock": map[string]any{"time": f.now.Format(time.RFC3339)}}})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOccupancy) set(t time.Time) { f.mu.Lock(); f.now = t; f.mu.Unlock() }

var t0 = time.Date(2026, 10, 13, 10, 30, 0, 0, time.UTC)

func TestNoDriftWhenBothClocksAdvanceTogether(t *testing.T) {
	f := newFakeOccupancy(t, t0)
	m := NewDriftMonitor(f.srv.URL, 10)
	if d, err := m.Measure(context.Background(), 0); err != nil || d != 0 {
		t.Fatalf("baseline: drift %v err %v", d, err)
	}
	f.set(t0.Add(600 * time.Second))
	d, err := m.Measure(context.Background(), 600)
	if err != nil || d != 0 {
		t.Fatalf("drift = %v (%v), want 0", d, err)
	}
}

func TestDriftIsPositiveWhenOccupancyRunsAhead(t *testing.T) {
	f := newFakeOccupancy(t, t0)
	m := NewDriftMonitor(f.srv.URL, 10)
	_, _ = m.Measure(context.Background(), 0)
	f.set(t0.Add(612 * time.Second)) // the people simulator is 12 s ahead of our 600
	d, _ := m.Measure(context.Background(), 600)
	if d != 12 {
		t.Fatalf("drift = %v, want +12", d)
	}
}

func TestDriftIsNegativeWhenOccupancyFallsBehind(t *testing.T) {
	f := newFakeOccupancy(t, t0)
	m := NewDriftMonitor(f.srv.URL, 10)
	_, _ = m.Measure(context.Background(), 100)
	f.set(t0.Add(94 * time.Second)) // 94 s on their clock while ours moved 100
	if d, _ := m.Measure(context.Background(), 200); d != -6 {
		t.Fatalf("drift = %v, want -6", d)
	}
}

func TestAnUnreachableOrGarbledClockIsAnErrorNotAFakeZero(t *testing.T) {
	f := newFakeOccupancy(t, t0)
	m := NewDriftMonitor(f.srv.URL, 10)
	f.mu.Lock()
	f.bad = true
	f.mu.Unlock()
	if _, err := m.Measure(context.Background(), 0); err == nil {
		t.Fatal("a garbled answer was accepted")
	}
	f.srv.Close()
	if _, err := m.Measure(context.Background(), 0); err == nil {
		t.Fatal("an unreachable simulator was reported as no drift")
	}
}

func TestStatusFlagsDriftBeyondTheThresholdAndRecovers(t *testing.T) {
	f := newFakeOccupancy(t, t0)
	m := NewDriftMonitor(f.srv.URL, 10)
	m.Warn = 10
	now := 0.0
	m.Sim = func() float64 { return now }
	ctx := context.Background()
	m.Poll(ctx)
	f.set(t0.Add(605 * time.Second))
	now = 600
	m.Poll(ctx)
	if st := m.Status(); st.Exceeded || st.DriftSec != 5 {
		t.Fatalf("5 s of drift flagged: %+v", st)
	}
	f.set(t0.Add(625 * time.Second))
	m.Poll(ctx)
	if st := m.Status(); !st.Exceeded || st.DriftSec != 25 {
		t.Fatalf("25 s of drift not flagged: %+v", st)
	}
	f.set(t0.Add(602 * time.Second))
	m.Poll(ctx)
	if st := m.Status(); st.Exceeded {
		t.Fatalf("recovery not seen: %+v", st)
	}
}

func TestStatusKeepsTheLastDriftAndSaysWhyWhenTheClockCannotBeRead(t *testing.T) {
	f := newFakeOccupancy(t, t0)
	m := NewDriftMonitor(f.srv.URL, 10)
	now := 0.0
	m.Sim = func() float64 { return now }
	m.Poll(context.Background())
	f.set(t0.Add(608 * time.Second))
	now = 600
	m.Poll(context.Background())
	f.srv.Close()
	m.Poll(context.Background())
	st := m.Status()
	if st.DriftSec != 8 || st.Error == "" {
		t.Fatalf("status = %+v, want the last drift (8) and an error", st)
	}
}

func TestControlStateIncludesTheClockDrift(t *testing.T) {
	e := testEngine()
	e.SetClockStatus(func() ClockStatus { return ClockStatus{DriftSec: 7.5, Exceeded: false} })
	rec := httptest.NewRecorder()
	e.ControlHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/state", nil))
	var st struct {
		ClockDrift *ClockStatus `json:"clock_drift"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.ClockDrift == nil || st.ClockDrift.DriftSec != 7.5 {
		t.Fatalf("state = %s", rec.Body)
	}
}
