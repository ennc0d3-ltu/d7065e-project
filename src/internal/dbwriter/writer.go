// Package dbwriter moves readings, decisions and commands from MQTT into the
// history store (decision D1). It sits off the safety path: if the database is
// down the detector and sprinklers carry on, and the writer buffers what it
// can (NFR6, degradation matrix). The buffer is bounded, and when it overflows
// it sheds old readings first, never the audit trail of decisions and commands.
package dbwriter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// Store is the history database.
type Store interface {
	InsertReadings(ctx context.Context, rs []gateway.Reading) error
	InsertDecisions(ctx context.Context, ds []proto.Decision) error
	InsertCommands(ctx context.Context, cs []proto.Command) error
	InsertDeadLetters(ctx context.Context, ds []proto.DeadLetter) error
	InsertDispatches(ctx context.Context, ds []proto.Dispatch, acks []proto.DispatchAck) error
	// InsertHeartbeats upserts the latest heartbeat per process (component status).
	InsertHeartbeats(ctx context.Context, hs []proto.Heartbeat) error
}

// Config sets the batching and the buffer bound.
type Config struct {
	BatchSize   int // readings per write; a full batch is written without waiting for a flush
	MaxBuffered int // records held while the store is unreachable, all kinds together
}

// Stats are the writer's counters.
type Stats struct {
	Readings    uint64 // readings written
	Decisions   uint64
	Commands    uint64
	DeadLetters uint64
	Dispatches  uint64
	Refreshes   uint64 // state refreshes seen and deliberately not stored
	Rejected    uint64 // malformed payloads
	Dropped     uint64 // readings shed on overflow
	FlushErrors uint64
	Pending     int // records buffered now
}

// Writer buffers and writes. It is safe for concurrent use.
type Writer struct {
	store Store
	cfg   Config

	mu        sync.Mutex
	readings  []gateway.Reading
	decisions []proto.Decision
	commands  []proto.Command
	dead      []proto.DeadLetter
	dispatch  []proto.Dispatch
	acks      []proto.DispatchAck
	beats     map[string]proto.Heartbeat // newest per process, not yet written
	stats     Stats
}

// New returns a writer for store.
func New(store Store, cfg Config) *Writer {
	return &Writer{store: store, cfg: cfg}
}

// Handle buffers one MQTT message: live and replayed readings and decisions,
// commands, and dead letters. A decision marked Refresh is a re-announcement,
// not a new decision, and is not stored. Other topics are ignored.
func (w *Writer) Handle(ctx context.Context, topic string, payload []byte) error {
	var flush bool
	topic = strings.TrimPrefix(topic, "replay/") // replayed history lands with the live history
	switch {
	case strings.HasPrefix(topic, "heartbeat/"):
		var hb proto.Heartbeat
		if err := json.Unmarshal(payload, &hb); err != nil || hb.Validate() != nil {
			return w.reject(topic, errors.New("bad heartbeat"))
		}
		w.mu.Lock()
		if w.beats == nil {
			w.beats = map[string]proto.Heartbeat{}
		}
		if old, ok := w.beats[hb.Process]; !ok || hb.Seq >= old.Seq || hb.SimTime >= old.SimTime {
			w.beats[hb.Process] = hb // only the newest per process is ever written
		}
		w.mu.Unlock()
	case topic == proto.DispatchTopic:
		var d proto.Dispatch
		if err := json.Unmarshal(payload, &d); err != nil {
			return w.reject(topic, err)
		}
		w.mu.Lock()
		w.dispatch = append(w.dispatch, d)
		w.mu.Unlock()
	case topic == proto.DispatchAckTopic:
		var a proto.DispatchAck
		if err := json.Unmarshal(payload, &a); err != nil {
			return w.reject(topic, err)
		}
		w.mu.Lock()
		w.acks = append(w.acks, a)
		w.mu.Unlock()
	case strings.HasPrefix(topic, "dlq/"):
		var d proto.DeadLetter
		if err := json.Unmarshal(payload, &d); err != nil {
			return w.reject(topic, err)
		}
		w.mu.Lock()
		w.dead = append(w.dead, d)
		w.shedLocked()
		w.mu.Unlock()
	case strings.HasPrefix(topic, "sensors/"):
		var r gateway.Reading
		if err := json.Unmarshal(payload, &r); err != nil {
			return w.reject(topic, err)
		}
		w.mu.Lock()
		w.readings = append(w.readings, r)
		flush = w.cfg.BatchSize > 0 && len(w.readings) >= w.cfg.BatchSize
		w.shedLocked()
		w.mu.Unlock()
	case strings.HasPrefix(topic, "decisions/"):
		var d proto.Decision
		if err := json.Unmarshal(payload, &d); err != nil {
			return w.reject(topic, err)
		}
		w.mu.Lock()
		if d.Refresh {
			w.stats.Refreshes++
			w.mu.Unlock()
			return nil
		}
		w.decisions = append(w.decisions, d)
		w.shedLocked()
		w.mu.Unlock()
	case strings.HasPrefix(topic, "commands/sprinkler/"):
		var c proto.Command
		if err := json.Unmarshal(payload, &c); err != nil {
			return w.reject(topic, err)
		}
		w.mu.Lock()
		w.commands = append(w.commands, c)
		w.shedLocked()
		w.mu.Unlock()
	default:
		return nil
	}
	if flush {
		_ = w.Flush(ctx) // a failure keeps the records; Stats shows it
	}
	return nil
}

func (w *Writer) reject(topic string, err error) error {
	w.mu.Lock()
	w.stats.Rejected++
	w.mu.Unlock()
	return fmt.Errorf("dbwriter: bad payload on %s: %w", topic, err)
}

// shedLocked enforces MaxBuffered by dropping the oldest readings. Decisions
// and commands are never dropped: they are the audit trail (NFR5).
func (w *Writer) shedLocked() {
	if w.cfg.MaxBuffered <= 0 {
		return
	}
	over := len(w.readings) + len(w.decisions) + len(w.commands) + len(w.dead) - w.cfg.MaxBuffered
	over = min(over, len(w.readings))
	if over > 0 {
		w.readings = w.readings[over:]
		w.stats.Dropped += uint64(over)
	}
}

// Flush writes everything buffered. Each kind is cleared only when its own
// write succeeds, so a partial failure never writes anything twice. It returns
// an error if any write failed; the failed records stay buffered.
func (w *Writer) Flush(ctx context.Context) error {
	w.mu.Lock()
	rs, ds, cs, dl := w.readings, w.decisions, w.commands, w.dead
	dp, ak := w.dispatch, w.acks
	bt := w.beats
	w.beats = nil
	w.readings, w.decisions, w.commands, w.dead = nil, nil, nil, nil
	w.dispatch, w.acks = nil, nil
	w.mu.Unlock()

	var errs []error
	keepR, keepD, keepC := []gateway.Reading(nil), []proto.Decision(nil), []proto.Command(nil)
	if len(rs) > 0 {
		if err := w.store.InsertReadings(ctx, rs); err != nil {
			errs, keepR = append(errs, err), rs
		} else {
			w.count(func(s *Stats) { s.Readings += uint64(len(rs)) })
		}
	}
	if len(ds) > 0 {
		if err := w.store.InsertDecisions(ctx, ds); err != nil {
			errs, keepD = append(errs, err), ds
		} else {
			w.count(func(s *Stats) { s.Decisions += uint64(len(ds)) })
		}
	}
	if len(cs) > 0 {
		if err := w.store.InsertCommands(ctx, cs); err != nil {
			errs, keepC = append(errs, err), cs
		} else {
			w.count(func(s *Stats) { s.Commands += uint64(len(cs)) })
		}
	}

	var keepDL []proto.DeadLetter
	if len(dl) > 0 {
		if err := w.store.InsertDeadLetters(ctx, dl); err != nil {
			errs, keepDL = append(errs, err), dl
		} else {
			w.count(func(s *Stats) { s.DeadLetters += uint64(len(dl)) })
		}
	}

	var keepDP []proto.Dispatch
	var keepAK []proto.DispatchAck
	if len(dp) > 0 || len(ak) > 0 {
		if err := w.store.InsertDispatches(ctx, dp, ak); err != nil {
			errs, keepDP, keepAK = append(errs, err), dp, ak
		} else {
			w.count(func(s *Stats) { s.Dispatches += uint64(len(dp)) })
		}
	}

	var keepBT map[string]proto.Heartbeat
	if len(bt) > 0 {
		hs := make([]proto.Heartbeat, 0, len(bt))
		for _, h := range bt {
			hs = append(hs, h)
		}
		if err := w.store.InsertHeartbeats(ctx, hs); err != nil {
			errs, keepBT = append(errs, err), bt
		}
	}

	// put failures back in front of anything that arrived meanwhile
	w.mu.Lock()
	w.readings = append(keepR, w.readings...)
	w.decisions = append(keepD, w.decisions...)
	w.commands = append(keepC, w.commands...)
	w.dead = append(keepDL, w.dead...)
	w.dispatch = append(keepDP, w.dispatch...)
	w.acks = append(keepAK, w.acks...)
	for p, h := range keepBT { // a newer beat that arrived meanwhile wins
		if w.beats == nil {
			w.beats = map[string]proto.Heartbeat{}
		}
		if _, newer := w.beats[p]; !newer {
			w.beats[p] = h
		}
	}
	if len(errs) > 0 {
		w.stats.FlushErrors++
	}
	w.shedLocked()
	w.mu.Unlock()
	return errors.Join(errs...)
}

func (w *Writer) count(f func(*Stats)) {
	w.mu.Lock()
	f(&w.stats)
	w.mu.Unlock()
}

// Stats returns a snapshot of the counters.
func (w *Writer) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.stats
	s.Pending = len(w.readings) + len(w.decisions) + len(w.commands) + len(w.dead) + len(w.dispatch) + len(w.acks)
	return s
}
