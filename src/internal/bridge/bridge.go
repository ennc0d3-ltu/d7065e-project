// Package bridge writes sensor readings from MQTT into BuildSim (decision
// D1) and applies the data-quality rules that belong on that edge: drop
// duplicates and stale messages, count sequence gaps (NFR12, H7).
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
)

// ErrMalformed marks a message that can never be applied (bad JSON, no
// sensor id): it belongs in the dead-letter queue, unlike an outage.
var ErrMalformed = errors.New("bridge: malformed reading")

// Writer is the part of the BuildSim client the bridge needs.
type Writer interface {
	PutSensorValue(ctx context.Context, id, value string) error
}

// Stats are the bridge's data-quality counters.
type Stats struct {
	Written       uint64 // readings written to BuildSim
	Duplicates    uint64 // dropped: seq already seen or out of order
	Gaps          uint64 // messages that never arrived (missing seq numbers)
	Rejected      uint64 // dropped: malformed payload
	WriteErrors   uint64 // BuildSim write failed
	NotRegistered uint64 // BuildSim does not know the sensor (yet)
}

type seen struct {
	seq     uint64
	simTime float64
}

// Bridge applies readings to BuildSim. It is safe for concurrent use.
type Bridge struct {
	w Writer

	mu    sync.Mutex
	last  map[string]seen
	stats Stats
}

// New returns a bridge that writes through w.
func New(w Writer) *Bridge {
	return &Bridge{w: w, last: map[string]seen{}}
}

// Stats returns a snapshot of the counters.
func (b *Bridge) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

// precision is the number of decimals kept per reading type.
var precision = map[string]int{"temperature": 2, "smoke": 4, "co": 1}

// Handle processes one MQTT message. A returned error means the reading did
// not reach BuildSim for a reason the caller may want to log; duplicates and
// unregistered sensors are normal and return nil.
func (b *Bridge) Handle(ctx context.Context, topic string, payload []byte) error {
	var r gateway.Reading
	if err := json.Unmarshal(payload, &r); err != nil {
		b.count(func(s *Stats) { s.Rejected++ })
		return fmt.Errorf("%w on %s: %v", ErrMalformed, topic, err)
	}
	if r.SensorID == "" {
		b.count(func(s *Stats) { s.Rejected++ })
		return fmt.Errorf("%w on %s: no sensor id", ErrMalformed, topic)
	}
	if !b.accept(r) {
		return nil
	}

	prec, ok := precision[r.Type]
	if !ok {
		prec = -1 // shortest exact form
	}
	err := b.w.PutSensorValue(ctx, r.SensorID, strconv.FormatFloat(r.Value, 'f', prec, 64))
	switch {
	case err == nil:
		b.count(func(s *Stats) { s.Written++ })
	case errors.Is(err, buildsim.ErrNotFound):
		b.count(func(s *Stats) { s.NotRegistered++ })
		return nil
	default:
		b.count(func(s *Stats) { s.WriteErrors++ })
		return err
	}
	return nil
}

// accept applies sequence rules and records the reading as seen. A sequence
// number going backwards while sim time moves forward means the sender
// restarted, so the baseline is reset rather than the reading dropped.
func (b *Bridge) accept(r gateway.Reading) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	prev, known := b.last[r.SensorID]
	switch {
	case !known:
		// first message seen: it is the baseline, not a gap
	case r.Seq > prev.seq:
		b.stats.Gaps += r.Seq - prev.seq - 1
	case r.SimTime > prev.simTime:
		// sender restarted: sequence reset, sim time still advancing
	default:
		b.stats.Duplicates++
		return false
	}
	b.last[r.SensorID] = seen{seq: r.Seq, simTime: r.SimTime}
	return true
}

func (b *Bridge) count(f func(*Stats)) {
	b.mu.Lock()
	f(&b.stats)
	b.mu.Unlock()
}
