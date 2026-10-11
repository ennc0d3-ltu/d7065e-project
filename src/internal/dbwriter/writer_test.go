package dbwriter

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

type fakeStore struct {
	mu        sync.Mutex
	readings  []gateway.Reading
	decisions []proto.Decision
	commands  []proto.Command
	dead      []proto.DeadLetter
	dispatch  []proto.Dispatch
	acks      []proto.DispatchAck
	beats     [][]proto.Heartbeat
	failB     bool
	failR     bool // fail the next reading insert
	failD     bool
	failC     bool
	calls     int
}

func (f *fakeStore) InsertReadings(_ context.Context, rs []gateway.Reading) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failR {
		return errors.New("db down")
	}
	f.readings = append(f.readings, rs...)
	return nil
}

func (f *fakeStore) InsertDecisions(_ context.Context, ds []proto.Decision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failD {
		return errors.New("db down")
	}
	f.decisions = append(f.decisions, ds...)
	return nil
}

func (f *fakeStore) InsertCommands(_ context.Context, cs []proto.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failC {
		return errors.New("db down")
	}
	f.commands = append(f.commands, cs...)
	return nil
}

func (f *fakeStore) InsertDeadLetters(_ context.Context, ds []proto.DeadLetter) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dead = append(f.dead, ds...)
	return nil
}

func (f *fakeStore) InsertDispatches(_ context.Context, ds []proto.Dispatch, as []proto.DispatchAck) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatch = append(f.dispatch, ds...)
	f.acks = append(f.acks, as...)
	return nil
}

func (f *fakeStore) InsertHeartbeats(_ context.Context, hs []proto.Heartbeat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failB {
		return errors.New("db down")
	}
	f.beats = append(f.beats, hs)
	return nil
}

func beat(process string, seq uint64) []byte {
	b, _ := json.Marshal(proto.Heartbeat{Process: process, Seq: seq, SimTime: float64(seq)})
	return b
}

func reading(seq uint64) []byte {
	b, _ := json.Marshal(gateway.Reading{SensorID: "s", Room: "level0/A109", Type: "smoke", Unit: "1/m", Value: 0.1, Seq: seq, SimTime: float64(seq)})
	return b
}

func decision(room string) []byte {
	b, _ := json.Marshal(proto.Decision{Room: room, SimTime: 5, Tier: "fire", State: "fire", Action: "actuate", Reason: "r"})
	return b
}

func command(seq uint64) []byte {
	b, _ := json.Marshal(proto.Command{Room: "level0/A109", Action: proto.ActionOn, Reason: "r", Seq: seq, TTL: 30, SimTime: 5})
	return b
}

var ctx = context.Background()

func TestReadingIsBufferedUntilFlush(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	if err := w.Handle(ctx, "sensors/level0/A109/smoke", reading(1)); err != nil {
		t.Fatal(err)
	}
	if len(st.readings) != 0 {
		t.Fatal("written before the flush")
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(st.readings) != 1 || st.readings[0].Seq != 1 {
		t.Fatalf("readings = %+v", st.readings)
	}
}

func TestABatchSizeTriggersAWriteWithoutAFlush(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 3, MaxBuffered: 1000})
	for i := uint64(1); i <= 3; i++ {
		_ = w.Handle(ctx, "sensors/x", reading(i))
	}
	if len(st.readings) != 3 {
		t.Fatalf("readings written = %d, want 3 after a full batch", len(st.readings))
	}
}

func TestDecisionsAndCommandsAreWritten(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	_ = w.Handle(ctx, "decisions/level0/A109", decision("level0/A109"))
	_ = w.Handle(ctx, "commands/sprinkler/level0/A109", command(1))
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(st.decisions) != 1 || st.decisions[0].Action != "actuate" || len(st.commands) != 1 || st.commands[0].Seq != 1 {
		t.Fatalf("decisions=%+v commands=%+v", st.decisions, st.commands)
	}
}

func TestStoreFailureKeepsEverythingForTheNextFlush(t *testing.T) {
	st := &fakeStore{failR: true, failD: true, failC: true}
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	_ = w.Handle(ctx, "sensors/x", reading(1))
	_ = w.Handle(ctx, "sensors/x", reading(2))
	_ = w.Handle(ctx, "decisions/x", decision("level0/A109"))
	_ = w.Handle(ctx, "commands/sprinkler/x", command(1))
	if err := w.Flush(ctx); err == nil {
		t.Fatal("expected an error while the database is down")
	}
	if w.Stats().Pending != 4 || w.Stats().FlushErrors == 0 {
		t.Fatalf("stats = %+v", w.Stats())
	}
	st.failR, st.failD, st.failC = false, false, false
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(st.readings) != 2 || st.readings[0].Seq != 1 || st.readings[1].Seq != 2 {
		t.Fatalf("readings = %+v, want seq 1,2 in order, once each", st.readings)
	}
	if len(st.decisions) != 1 || len(st.commands) != 1 || w.Stats().Pending != 0 {
		t.Fatalf("decisions=%d commands=%d stats=%+v", len(st.decisions), len(st.commands), w.Stats())
	}
}

func TestAPartialFailureDoesNotWriteTheSuccessfulPartTwice(t *testing.T) {
	st := &fakeStore{failD: true} // readings succeed, decisions fail
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	_ = w.Handle(ctx, "sensors/x", reading(1))
	_ = w.Handle(ctx, "decisions/x", decision("level0/A109"))
	_ = w.Flush(ctx)
	st.failD = false
	_ = w.Flush(ctx)
	if len(st.readings) != 1 {
		t.Fatalf("readings written %d times, want exactly once", len(st.readings))
	}
	if len(st.decisions) != 1 {
		t.Fatalf("decisions = %d, want 1 after the retry", len(st.decisions))
	}
}

func TestOverflowShedsOldReadingsButNeverTheAuditTrail(t *testing.T) {
	st := &fakeStore{failR: true, failD: true, failC: true}
	w := New(st, Config{BatchSize: 1000, MaxBuffered: 5})
	_ = w.Handle(ctx, "decisions/x", decision("level0/A109"))
	_ = w.Handle(ctx, "commands/sprinkler/x", command(1))
	for i := uint64(1); i <= 10; i++ {
		_ = w.Handle(ctx, "sensors/x", reading(i))
	}
	s := w.Stats()
	if s.Dropped != 7 { // 12 records buffered, cap 5, only readings may be shed
		t.Fatalf("dropped = %d, want 7", s.Dropped)
	}
	st.failR, st.failD, st.failC = false, false, false
	_ = w.Flush(ctx)
	if len(st.decisions) != 1 || len(st.commands) != 1 {
		t.Fatalf("the audit trail lost records: decisions=%d commands=%d", len(st.decisions), len(st.commands))
	}
	if len(st.readings) != 3 || st.readings[0].Seq != 8 {
		t.Fatalf("readings = %+v, want the newest three (seq 8..10)", st.readings)
	}
}

func TestMalformedPayloadIsRejectedAndCounted(t *testing.T) {
	w := New(&fakeStore{}, Config{BatchSize: 10, MaxBuffered: 100})
	if err := w.Handle(ctx, "sensors/x", []byte(`nope`)); err == nil {
		t.Fatal("expected an error")
	}
	if w.Stats().Rejected != 1 {
		t.Fatalf("stats = %+v", w.Stats())
	}
}

func TestOtherTopicsAreIgnored(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 10, MaxBuffered: 100})
	if err := w.Handle(ctx, "heartbeat/detector", []byte(`{"process":"detector","seq":1}`)); err != nil {
		t.Fatalf("a heartbeat must be ignored quietly: %v", err)
	}
	_ = w.Flush(ctx)
	if st.calls != 0 || w.Stats().Pending != 0 {
		t.Fatalf("calls=%d stats=%+v", st.calls, w.Stats())
	}
}

func TestEmptyFlushDoesNotCallTheStore(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 10, MaxBuffered: 100})
	if err := w.Flush(ctx); err != nil || st.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, st.calls)
	}
}

func TestReplayedReadingsAndDecisionsFillTheHistory(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	_ = w.Handle(ctx, "replay/sensors/level0/A109/smoke", reading(7))
	_ = w.Handle(ctx, "replay/decisions/level0/A109", decision("level0/A109"))
	_ = w.Flush(ctx)
	if len(st.readings) != 1 || st.readings[0].Seq != 7 || len(st.decisions) != 1 {
		t.Fatalf("readings=%+v decisions=%+v", st.readings, st.decisions)
	}
}

func TestStateRefreshesAreNotStoredAsDecisions(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	b, _ := json.Marshal(proto.Decision{Room: "level0/A109", SimTime: 5, State: "fire", Action: "none", Reason: "state refresh", Refresh: true})
	if err := w.Handle(ctx, "decisions/level0/A109", b); err != nil {
		t.Fatal(err)
	}
	_ = w.Flush(ctx)
	if len(st.decisions) != 0 {
		t.Fatalf("a refresh was stored in the audit trail: %+v", st.decisions)
	}
}

func TestDeadLettersAreStoredAndNeverShed(t *testing.T) {
	st := &fakeStore{failR: true}
	w := New(st, Config{BatchSize: 1000, MaxBuffered: 3})
	dl, _ := json.Marshal(proto.DeadLetter{Process: "bridge", Topic: "sensors/x", Payload: "nope", Error: "bad payload", WallTS: "2026-10-08T00:00:00Z"})
	_ = w.Handle(ctx, "dlq/bridge", dl)
	for i := uint64(1); i <= 10; i++ {
		_ = w.Handle(ctx, "sensors/x", reading(i))
	}
	st.failR = false
	_ = w.Flush(ctx)
	if len(st.dead) != 1 || st.dead[0].Process != "bridge" || st.dead[0].Error != "bad payload" {
		t.Fatalf("dead letters = %+v", st.dead)
	}
}

func TestDispatchesAndAcknowledgementsAreStored(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	d, _ := json.Marshal(proto.Dispatch{Incident: "inc-1", Kind: "fire", Severity: "critical", SimTime: 100,
		Affected: []string{"level0/A1008"}, InDanger: []proto.PersonRef{{ID: "p1", Name: "Alice", Room: "level0/A1008"}}})
	a, _ := json.Marshal(proto.DispatchAck{Incident: "inc-1", Responder: "mock-responder", SimTime: 110})
	_ = w.Handle(ctx, proto.DispatchTopic, d)
	_ = w.Handle(ctx, proto.DispatchAckTopic, a)
	_ = w.Flush(ctx)
	if len(st.dispatch) != 1 || st.dispatch[0].Incident != "inc-1" || len(st.acks) != 1 || st.acks[0].SimTime != 110 {
		t.Fatalf("dispatch=%+v acks=%+v", st.dispatch, st.acks)
	}
}

func TestDispatchesAreNeverShedAndRetriedAfterAFailure(t *testing.T) {
	st := &failingDispatchStore{fakeStore: &fakeStore{}}
	w := New(st, Config{BatchSize: 1000, MaxBuffered: 2})
	d, _ := json.Marshal(proto.Dispatch{Incident: "inc-1", Kind: "fire", SimTime: 1})
	_ = w.Handle(ctx, proto.DispatchTopic, d)
	for i := uint64(1); i <= 5; i++ {
		_ = w.Handle(ctx, "sensors/x", reading(i))
	}
	_ = w.Flush(ctx) // dispatch insert fails
	st.fail = false
	_ = w.Flush(ctx)
	if len(st.dispatch) != 1 {
		t.Fatalf("dispatch lost across a failed write and overflow: %+v", st.dispatch)
	}
}

type failingDispatchStore struct {
	*fakeStore
	fail bool
}

func (f *failingDispatchStore) InsertDispatches(ctx context.Context, ds []proto.Dispatch, as []proto.DispatchAck) error {
	if f.fail {
		return errors.New("db down")
	}
	return f.fakeStore.InsertDispatches(ctx, ds, as)
}

func TestOnlyTheLatestHeartbeatPerProcessIsStored(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	for seq := uint64(1); seq <= 50; seq++ {
		_ = w.Handle(ctx, "heartbeat/bridge", beat("bridge", seq))
		_ = w.Handle(ctx, "heartbeat/detector", beat("detector", seq*2))
	}
	_ = w.Flush(ctx)
	if len(st.beats) != 1 || len(st.beats[0]) != 2 {
		t.Fatalf("writes = %+v, want one write of two rows", st.beats)
	}
	got := map[string]uint64{}
	for _, h := range st.beats[0] {
		got[h.Process] = h.Seq
	}
	if got["bridge"] != 50 || got["detector"] != 100 {
		t.Fatalf("stored = %v, want only the newest of each", got)
	}
}

func TestHeartbeatsAreNotRewrittenWhenNothingNewArrived(t *testing.T) {
	st := &fakeStore{}
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	_ = w.Handle(ctx, "heartbeat/bridge", beat("bridge", 1))
	_ = w.Flush(ctx)
	_ = w.Flush(ctx)
	if len(st.beats) != 1 {
		t.Fatalf("heartbeat written %d times for one beat", len(st.beats))
	}
}

func TestAFailedHeartbeatWriteKeepsTheNewestForTheNextFlush(t *testing.T) {
	st := &fakeStore{failB: true}
	w := New(st, Config{BatchSize: 100, MaxBuffered: 1000})
	_ = w.Handle(ctx, "heartbeat/bridge", beat("bridge", 1))
	_ = w.Flush(ctx)
	_ = w.Handle(ctx, "heartbeat/bridge", beat("bridge", 2))
	st.failB = false
	_ = w.Flush(ctx)
	if len(st.beats) != 1 || st.beats[0][0].Seq != 2 {
		t.Fatalf("writes = %+v, want the newest beat (seq 2) once", st.beats)
	}
}

func TestWriterIsSafeForConcurrentUse(t *testing.T) {
	w := New(&fakeStore{}, Config{BatchSize: 7, MaxBuffered: 10000})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := uint64(1); i <= 200; i++ {
				_ = w.Handle(ctx, "sensors/x", reading(i))
				if i%50 == 0 {
					_ = w.Flush(ctx)
				}
			}
		}()
	}
	wg.Wait()
	_ = w.Flush(ctx)
	if got := w.Stats().Readings; got != 800 {
		t.Fatalf("readings written = %d, want 800", got)
	}
}
