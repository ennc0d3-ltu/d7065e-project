package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
)

type fakeWriter struct {
	writes []struct{ id, value string }
	err    error
}

func (f *fakeWriter) PutSensorValue(_ context.Context, id, value string) error {
	if f.err != nil {
		return f.err
	}
	f.writes = append(f.writes, struct{ id, value string }{id, value})
	return nil
}

func reading(id, typ string, v float64, seq uint64) []byte {
	b, _ := json.Marshal(gateway.Reading{
		SensorID: id, Room: "level0/A109", Type: typ, Unit: "x", Value: v, Seq: seq, SimTime: float64(seq),
	})
	return b
}

func TestWritesReadingToBuildSimWithFixedPrecision(t *testing.T) {
	w := &fakeWriter{}
	b := New(w)
	ctx := context.Background()
	_ = b.Handle(ctx, "sensors/level0/A109/temperature", reading("t", "temperature", 21.456, 1))
	_ = b.Handle(ctx, "sensors/level0/A109/smoke", reading("s", "smoke", 0.04567, 1))
	_ = b.Handle(ctx, "sensors/level0/A109/co", reading("c", "co", 12.34, 1))
	want := []struct{ id, value string }{{"t", "21.46"}, {"s", "0.0457"}, {"c", "12.3"}}
	if len(w.writes) != 3 {
		t.Fatalf("writes = %v", w.writes)
	}
	for i, x := range want {
		if w.writes[i] != x {
			t.Errorf("write %d = %v, want %v", i, w.writes[i], x)
		}
	}
}

func TestDuplicateAndOutOfOrderReadingsAreDropped(t *testing.T) {
	w := &fakeWriter{}
	b := New(w)
	ctx := context.Background()
	for _, seq := range []uint64{1, 2, 2, 1, 3} {
		_ = b.Handle(ctx, "sensors/x", reading("t", "temperature", float64(seq), seq))
	}
	if len(w.writes) != 3 {
		t.Fatalf("wrote %d readings, want 3 (seq 1,2,3)", len(w.writes))
	}
	s := b.Stats()
	if s.Duplicates != 2 {
		t.Fatalf("duplicates = %d, want 2", s.Duplicates)
	}
}

func TestSequenceGapsAreCounted(t *testing.T) {
	b := New(&fakeWriter{})
	ctx := context.Background()
	for _, seq := range []uint64{1, 2, 5, 6} { // 3 and 4 never arrived
		_ = b.Handle(ctx, "sensors/x", reading("t", "temperature", 1, seq))
	}
	if got := b.Stats().Gaps; got != 2 {
		t.Fatalf("gaps = %d, want 2 missing messages", got)
	}
}

func TestSenderRestartDoesNotSilenceTheSensor(t *testing.T) {
	w := &fakeWriter{}
	b := New(w)
	ctx := context.Background()
	for _, seq := range []uint64{1, 2, 3} {
		_ = b.Handle(ctx, "sensors/x", reading("t", "temperature", 1, seq))
	}
	// The gateway restarted: its counter is back at 1, but sim time moved on.
	restarted, _ := json.Marshal(gateway.Reading{
		SensorID: "t", Room: "level0/A109", Type: "temperature", Value: 2, Seq: 1, SimTime: 100,
	})
	if err := b.Handle(ctx, "sensors/x", restarted); err != nil {
		t.Fatal(err)
	}
	if len(w.writes) != 4 {
		t.Fatalf("wrote %d readings, want 4: a restarted sender must not look like duplicates", len(w.writes))
	}
	if b.Stats().Duplicates != 0 || b.Stats().Gaps != 0 {
		t.Fatalf("stats = %+v", b.Stats())
	}
}

func TestEachSensorHasItsOwnSequence(t *testing.T) {
	w := &fakeWriter{}
	b := New(w)
	ctx := context.Background()
	_ = b.Handle(ctx, "sensors/x", reading("a", "temperature", 1, 1))
	_ = b.Handle(ctx, "sensors/x", reading("b", "temperature", 1, 1)) // same seq, other sensor
	if len(w.writes) != 2 {
		t.Fatalf("writes = %d, want 2", len(w.writes))
	}
}

func TestMalformedPayloadIsRejectedAndCounted(t *testing.T) {
	w := &fakeWriter{}
	b := New(w)
	if err := b.Handle(context.Background(), "sensors/x", []byte(`not json`)); err == nil {
		t.Fatal("expected an error")
	}
	if len(w.writes) != 0 || b.Stats().Rejected != 1 {
		t.Fatalf("writes=%d stats=%+v", len(w.writes), b.Stats())
	}
}

func TestOnlyABadMessageIsMarkedMalformed(t *testing.T) {
	b := New(&fakeWriter{})
	for _, payload := range [][]byte{[]byte(`nope`), reading("", "temperature", 1, 1)} {
		if err := b.Handle(context.Background(), "sensors/x", payload); !errors.Is(err, ErrMalformed) {
			t.Errorf("err = %v, want ErrMalformed for %s", err, payload)
		}
	}
	// BuildSim being down is an outage, not a bad message: no dead letter
	down := New(&fakeWriter{err: errors.New("buildsim down")})
	if err := down.Handle(context.Background(), "sensors/x", reading("t", "temperature", 1, 1)); errors.Is(err, ErrMalformed) {
		t.Fatal("an outage was marked as a malformed message")
	}
}

func TestReadingWithoutSensorIDIsRejected(t *testing.T) {
	b := New(&fakeWriter{})
	if err := b.Handle(context.Background(), "sensors/x", reading("", "temperature", 1, 1)); err == nil {
		t.Fatal("expected an error for an empty sensor id")
	}
}

func TestBuildSimFailureIsReportedWithoutLosingTheSequence(t *testing.T) {
	w := &fakeWriter{err: errors.New("buildsim down")}
	b := New(w)
	ctx := context.Background()
	if err := b.Handle(ctx, "sensors/x", reading("t", "temperature", 1, 1)); err == nil {
		t.Fatal("expected the write error")
	}
	if b.Stats().WriteErrors != 1 {
		t.Fatalf("stats = %+v", b.Stats())
	}
	// BuildSim comes back: the next reading is written, and nothing looks like a gap
	w.err = nil
	_ = b.Handle(ctx, "sensors/x", reading("t", "temperature", 2, 2))
	if len(w.writes) != 1 || b.Stats().Gaps != 0 {
		t.Fatalf("writes=%d stats=%+v", len(w.writes), b.Stats())
	}
}

func TestUnregisteredSensorIsNotAnError(t *testing.T) {
	w := &fakeWriter{err: buildsim.ErrNotFound}
	b := New(w)
	if err := b.Handle(context.Background(), "sensors/x", reading("t", "temperature", 1, 1)); err != nil {
		t.Fatalf("a sensor not yet registered must not fail the bridge: %v", err)
	}
	if b.Stats().NotRegistered != 1 {
		t.Fatalf("stats = %+v", b.Stats())
	}
}
