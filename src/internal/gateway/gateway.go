package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Kind is the physical quantity a sensor measures.
type Kind string

const (
	KindTemperature Kind = "temperature"
	KindSmoke       Kind = "smoke"
	KindCO          Kind = "co"
)

var units = map[Kind]string{KindTemperature: "°C", KindSmoke: "1/m", KindCO: "ppm"}

// Truth is one room's ground truth, as served by physics-sim.
type Truth struct {
	TempC   float64 `json:"temp_c"`
	Smoke   float64 `json:"smoke"`
	COppm   float64 `json:"co_ppm"`
	SimTime float64 `json:"sim_time"`
}

func (t Truth) value(k Kind) float64 {
	switch k {
	case KindSmoke:
		return t.Smoke
	case KindCO:
		return t.COppm
	default:
		return t.TempC
	}
}

// Reading is the message a sensor publishes (FR1): units explicit, a
// per-sensor sequence number and a sim-time timestamp.
type Reading struct {
	SensorID string  `json:"sensor_id"`
	Room     string  `json:"room"`
	Type     string  `json:"type"`
	Unit     string  `json:"unit"`
	Value    float64 `json:"value"`
	Seq      uint64  `json:"seq"`
	SimTime  float64 `json:"sim_time"`
}

// TruthSource reads ground truth (the only window onto the truth layer).
type TruthSource interface {
	Truth(ctx context.Context, room string) (Truth, error)
}

// Sink receives readings; in production it is the MQTT publisher.
type Sink interface {
	Publish(ctx context.Context, r Reading) error
}

// SensorSpec configures one simulated sensor.
type SensorSpec struct {
	ID    string
	Room  string // "<level>/<room>"
	Kind  Kind
	Sigma float64 // noise std-dev in sensor units
	Lag   float64 // sim seconds
	Seed  int64
}

type sensor struct {
	spec SensorSpec
	deg  *Degrader
	seq  uint64
}

// ErrUnknownSensor is returned when a fault names a sensor that does not exist.
var ErrUnknownSensor = errors.New("gateway: unknown sensor")

// Gateway samples truth for a set of sensors, degrades it, and publishes.
type Gateway struct {
	src  TruthSource
	sink Sink

	mu      sync.Mutex
	sensors []*sensor
	byID    map[string]*sensor
	simNow  float64
}

// New builds a gateway for the given sensors.
func New(specs []SensorSpec, src TruthSource, sink Sink) *Gateway {
	g := &Gateway{src: src, sink: sink, byID: map[string]*sensor{}}
	for _, sp := range specs {
		s := &sensor{spec: sp, deg: NewDegrader(sp.Sigma, sp.Lag, sp.Seed)}
		g.sensors = append(g.sensors, s)
		g.byID[sp.ID] = s
	}
	return g
}

// Sample reads each room's truth once, then publishes one reading per
// sensor that is working. It returns how many readings were published and
// how many things failed (truth fetches and publishes).
//
// The sequence number is the sensor's own counter and advances before the
// publish, so a lost message leaves a visible gap (H7).
func (g *Gateway) Sample(ctx context.Context) (published, errs int) {
	truths := map[string]Truth{}
	failed := map[string]bool{}
	for _, s := range g.sensors {
		room := s.spec.Room
		if _, done := truths[room]; done || failed[room] {
			continue
		}
		tr, err := g.src.Truth(ctx, room)
		if err != nil {
			failed[room] = true
			errs++
			continue
		}
		truths[room] = tr
	}

	g.mu.Lock()
	var out []Reading
	for _, s := range g.sensors {
		tr, ok := truths[s.spec.Room]
		if !ok {
			continue
		}
		g.simNow = max(g.simNow, tr.SimTime)
		v, ok := s.deg.Observe(tr.SimTime, tr.value(s.spec.Kind))
		if !ok {
			continue
		}
		if s.spec.Kind != KindTemperature {
			v = max(0, v) // concentrations cannot be negative; noise can push them there
		}
		s.seq++
		out = append(out, Reading{
			SensorID: s.spec.ID, Room: s.spec.Room, Type: string(s.spec.Kind),
			Unit: units[s.spec.Kind], Value: v, Seq: s.seq, SimTime: tr.SimTime,
		})
	}
	g.mu.Unlock()

	for _, r := range out {
		if err := g.sink.Publish(ctx, r); err != nil {
			errs++
			continue
		}
		published++
	}
	return published, errs
}

// SetFault injects (or with FaultNone clears) a failure on one sensor at the
// current sim time. driftRate is used only for FaultDrift.
func (g *Gateway) SetFault(sensorID string, f Fault, driftRate float64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.byID[sensorID]
	if !ok {
		return ErrUnknownSensor
	}
	s.deg.SetDrift(driftRate)
	s.deg.SetFault(f, g.simNow)
	return nil
}

var faultModes = map[string]Fault{
	"none": FaultNone, "stuck": FaultStuck, "offline": FaultOffline, "drift": FaultDrift,
}

// FaultHandler serves POST /faults/{sensor} with {"mode": "...", "drift_rate": n}
// so tests and demos can break a sensor while the system runs.
func (g *Gateway) FaultHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /faults/{sensor}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode      string  `json:"mode"`
			DriftRate float64 `json:"drift_rate"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		mode, ok := faultModes[body.Mode]
		if !ok {
			http.Error(w, "mode must be none, stuck, offline or drift", http.StatusBadRequest)
			return
		}
		if err := g.SetFault(r.PathValue("sensor"), mode, body.DriftRate); err != nil {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// TruthClient reads physics-sim's truth endpoint.
type TruthClient struct {
	base string
	http *http.Client
}

// NewTruthClient returns a client for a physics-sim at baseURL.
func NewTruthClient(baseURL string) *TruthClient {
	return &TruthClient{base: baseURL, http: &http.Client{Timeout: 2 * time.Second}}
}

// Truth fetches one room's ground truth.
func (c *TruthClient) Truth(ctx context.Context, room string) (Truth, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/truth/rooms/"+room, nil)
	if err != nil {
		return Truth{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Truth{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Truth{}, fmt.Errorf("truth %q: HTTP %d", room, resp.StatusCode)
	}
	var t Truth
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return Truth{}, err
	}
	return t, nil
}
