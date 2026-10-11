package detectsvc

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

type sent struct {
	topic   string
	payload []byte
}

type capture struct {
	msgs    []sent
	failing int // fail the next n publishes
}

func (c *capture) Publish(_ context.Context, topic string, _ byte, payload []byte) error {
	if c.failing > 0 {
		c.failing--
		return errors.New("broker down")
	}
	c.msgs = append(c.msgs, sent{topic, payload})
	return nil
}

func (c *capture) commands(t *testing.T, room string) []proto.Command {
	t.Helper()
	var out []proto.Command
	for _, m := range c.msgs {
		if m.topic == proto.CommandTopic(room) {
			var cmd proto.Command
			if err := json.Unmarshal(m.payload, &cmd); err != nil {
				t.Fatal(err)
			}
			out = append(out, cmd)
		}
	}
	return out
}

func (c *capture) decisions(t *testing.T, room string) []Decision {
	t.Helper()
	var out []Decision
	for _, m := range c.msgs {
		if m.topic == proto.DecisionTopic(room) {
			var d Decision
			if err := json.Unmarshal(m.payload, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
	}
	return out
}

func newSvc(pub Publisher) *Service {
	return New(DefaultConfig(), nil, detector.DefaultThresholds(), pub) // nil model: rule baseline
}

const roomA, roomB = "level0/A109", "level0/A108"

func feed(t *testing.T, s *Service, room string, now, smoke, co, temp float64) {
	t.Helper()
	for _, r := range []gateway.Reading{
		{SensorID: "s", Room: room, Type: "smoke", Value: smoke, SimTime: now, Seq: uint64(now) + 1},
		{SensorID: "c", Room: room, Type: "co", Value: co, SimTime: now, Seq: uint64(now) + 1},
		{SensorID: "t", Room: room, Type: "temperature", Value: temp, SimTime: now, Seq: uint64(now) + 1},
	} {
		raw, _ := json.Marshal(r)
		if err := s.Handle(context.Background(), "sensors/"+room+"/"+r.Type, raw); err != nil {
			t.Fatal(err)
		}
	}
}

func fire(t *testing.T, s *Service, room string, now float64) { feed(t, s, room, now, 0.5, 300, 60) }
func calm(t *testing.T, s *Service, room string, now float64) { feed(t, s, room, now, 0, 3, 21) }

func tick(t *testing.T, s *Service, now float64) {
	t.Helper()
	s.Tick(context.Background(), now)
}

func TestQuietRoomPublishesNoCommand(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 60; now += 5 {
		calm(t, s, roomA, now)
		tick(t, s, now)
	}
	if got := pub.commands(t, roomA); len(got) != 0 {
		t.Fatalf("quiet room got commands: %+v", got)
	}
}

func TestPersistentFireCommandsTheSprinklerOn(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 30; now += 5 {
		fire(t, s, roomA, now)
		tick(t, s, now)
	}
	cmds := pub.commands(t, roomA)
	if len(cmds) == 0 {
		t.Fatal("no command after a persistent fire")
	}
	first := cmds[0]
	if first.Action != proto.ActionOn || first.SimTime != 15 || first.Reason == "" {
		t.Fatalf("first command = %+v, want on at t=15 with a reason", first)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("detector published an invalid command: %v", err)
	}
}

func TestNoCommandBeforeThePersistenceWindow(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 10; now += 5 { // 10 s of fire tier < 15 s
		fire(t, s, roomA, now)
		tick(t, s, now)
	}
	if got := pub.commands(t, roomA); len(got) != 0 {
		t.Fatalf("commanded after only 10 s: %+v", got)
	}
}

func TestCommandIsResentWhileLatchedWithRisingSequence(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 40; now += 5 {
		fire(t, s, roomA, now)
		tick(t, s, now)
	}
	cmds := pub.commands(t, roomA)
	if len(cmds) < 3 {
		t.Fatalf("only %d commands: the actuator's TTL needs a heartbeat", len(cmds))
	}
	for i := 1; i < len(cmds); i++ {
		if cmds[i].Seq <= cmds[i-1].Seq {
			t.Fatalf("sequence did not rise: %+v", cmds)
		}
		if cmds[i].Action != proto.ActionOn || cmds[i].TTL <= 0 {
			t.Fatalf("bad refresh: %+v", cmds[i])
		}
	}
}

func TestSprinklerStaysOnUntilTheAllClearThenIsCommandedOff(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 20; now += 5 {
		fire(t, s, roomA, now)
		tick(t, s, now)
	}
	for now := 25.0; now <= 600; now += 5 {
		calm(t, s, roomA, now)
		tick(t, s, now)
	}
	var offAt float64 = -1
	for _, c := range pub.commands(t, roomA) {
		if c.Action == proto.ActionOff {
			if offAt >= 0 {
				t.Fatalf("more than one off command: %+v", pub.commands(t, roomA))
			}
			offAt = c.SimTime
		}
	}
	// last non-none tier was at t=20; ClearHold 300 s
	if offAt < 320 {
		t.Fatalf("sprinkler commanded off at t=%v, before the all-clear at 320", offAt)
	}
}

func TestAStaleSensorRaisesAWarningNeverNormal(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 30; now += 5 {
		// temperature never arrives
		for _, r := range []gateway.Reading{
			{SensorID: "s", Room: roomA, Type: "smoke", Value: 0, SimTime: now, Seq: uint64(now) + 1},
			{SensorID: "c", Room: roomA, Type: "co", Value: 3, SimTime: now, Seq: uint64(now) + 1},
		} {
			raw, _ := json.Marshal(r)
			_ = s.Handle(context.Background(), "sensors/x", raw)
		}
		tick(t, s, now)
	}
	ds := pub.decisions(t, roomA)
	if len(ds) == 0 {
		t.Fatal("no decision published for a room with a dead sensor")
	}
	if ds[0].State != "warning" || len(ds[0].Stale) != 1 || ds[0].Stale[0] != "temperature" {
		t.Fatalf("decision = %+v, want a warning naming the stale temperature signal", ds[0])
	}
	if got := pub.commands(t, roomA); len(got) != 0 {
		t.Fatalf("a stale sensor alone must not command the sprinkler: %+v", got)
	}
}

func TestDustAloneNeverCommandsTheSprinkler(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 600; now += 5 {
		feed(t, s, roomA, now, 0.5, 3, 21) // smoke only
		tick(t, s, now)
	}
	// a smoke-only warning that persists for ten minutes must still never
	// discharge a sprinkler: only an armed room (smoke and CO, no heat) has a
	// persistence valve (found by the system test T3)
	if got := pub.commands(t, roomA); len(got) != 0 {
		t.Fatalf("dust commanded the sprinkler: %+v", got[0])
	}
}

// quietModel trains a model on a tight cluster of quiet-room vectors, so any
// smoke, CO or heat is far outside what it has seen.
func quietModel(t *testing.T) *detector.Model {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	train := make([][]float64, 600)
	for i := range train {
		train[i] = []float64{
			0.01 + rng.NormFloat64()*0.005, 3 + rng.NormFloat64(), 21 + rng.NormFloat64()*0.3,
			rng.NormFloat64() * 1e-4, rng.NormFloat64() * 0.01, rng.NormFloat64() * 0.005,
		}
	}
	f, err := detector.Train(train, 100, 256, 7)
	if err != nil {
		t.Fatal(err)
	}
	return detector.NewModel(f, train)
}

func newModelSvc(t *testing.T, pub Publisher) *Service {
	return New(DefaultConfig(), quietModel(t), detector.DefaultThresholds(), pub)
}

// drift is what a neighbouring room sees: fire-level smoke and CO, no heat.
func drift(t *testing.T, s *Service, room string, now float64) { feed(t, s, room, now, 5, 500, 21) }

func TestModelAlsoNeedsHeatBeforeDischarging(t *testing.T) {
	pub := &capture{}
	s := newModelSvc(t, pub)
	for now := 0.0; now <= 100; now += 5 { // 100 s of smoke and CO, never any heat
		drift(t, s, roomA, now)
		tick(t, s, now)
	}
	if got := pub.commands(t, roomA); len(got) != 0 {
		t.Fatalf("the model discharged a sprinkler on smoke and CO alone: %+v", got)
	}
	if st := s.Rooms()[0].State; st != "warning" {
		t.Fatalf("state = %q, want warning (armed)", st)
	}
}

func TestModelDischargesOnceHeatAppears(t *testing.T) {
	pub := &capture{}
	s := newModelSvc(t, pub)
	for now := 0.0; now <= 40; now += 5 {
		feed(t, s, roomA, now, 5, 500, 21+float64(now)) // temperature climbing fast
		tick(t, s, now)
	}
	if got := pub.commands(t, roomA); len(got) == 0 || got[0].Action != proto.ActionOn {
		t.Fatalf("no discharge although the room is heating up: %+v", got)
	}
}

func TestPersistentSmokeWithoutHeatStillEscalatesAsASafetyValve(t *testing.T) {
	for _, tc := range []struct {
		name string
		svc  func(*testing.T, Publisher) *Service
	}{
		{"rules", func(_ *testing.T, p Publisher) *Service { return newSvc(p) }},
		{"model", newModelSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub := &capture{}
			s := tc.svc(t, pub)
			for now := 0.0; now <= 200; now += 5 {
				drift(t, s, roomA, now)
				tick(t, s, now)
			}
			cmds := pub.commands(t, roomA)
			if len(cmds) == 0 {
				t.Fatal("smoke and CO persisting for 200 s never escalated: a heat sensor that lags must not hide a fire")
			}
			if cmds[0].SimTime < 120 {
				t.Fatalf("escalated at t=%v, before the 120 s persistence window", cmds[0].SimTime)
			}
		})
	}
}

func TestRoomsAreIndependent(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 30; now += 5 {
		fire(t, s, roomA, now)
		calm(t, s, roomB, now)
		tick(t, s, now)
	}
	if len(pub.commands(t, roomA)) == 0 {
		t.Fatal("fire room was not commanded")
	}
	if got := pub.commands(t, roomB); len(got) != 0 {
		t.Fatalf("quiet neighbour was commanded: %+v", got)
	}
}

func TestOperatorConfirmCommandsTheSprinklerAtOnce(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	feed(t, s, roomA, 0, 0.5, 3, 21) // a warning
	tick(t, s, 0)
	if err := s.Confirm(context.Background(), roomA); err != nil {
		t.Fatal(err)
	}
	cmds := pub.commands(t, roomA)
	if len(cmds) != 1 || cmds[0].Action != proto.ActionOn || !strings.Contains(cmds[0].Reason, "operator") {
		t.Fatalf("commands = %+v", cmds)
	}
}

func TestOperatorActionOnAnUnknownRoomFails(t *testing.T) {
	s := newSvc(&capture{})
	if err := s.Confirm(context.Background(), "level9/none"); !errors.Is(err, ErrUnknownRoom) {
		t.Fatalf("err = %v, want ErrUnknownRoom", err)
	}
}

func TestMalformedReadingIsRejectedWithoutSideEffects(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	if err := s.Handle(context.Background(), "sensors/x", []byte(`nope`)); err == nil {
		t.Fatal("expected an error")
	}
	tick(t, s, 5)
	for _, m := range pub.msgs {
		if m.topic != proto.HeartbeatTopic("detector") {
			t.Fatalf("a bad reading produced output: %+v", m)
		}
	}
}

func TestAFailedCommandPublishIsRetriedOnTheNextTick(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 15; now += 5 {
		fire(t, s, roomA, now)
		if now == 15 {
			pub.failing = 2 // the broker drops the actuation: the decision, then the command
		}
		tick(t, s, now)
	}
	if len(pub.commands(t, roomA)) != 0 {
		t.Fatal("test setup: the first command should have been lost")
	}
	fire(t, s, roomA, 20)
	tick(t, s, 20)
	if got := pub.commands(t, roomA); len(got) == 0 || got[0].Action != proto.ActionOn {
		t.Fatalf("the command was not re-sent after a publish failure: %+v", got)
	}
}

func TestDetectorSendsAHeartbeatOnEveryTickEvenWhenIdle(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 20; now += 5 {
		calm(t, s, roomA, now)
		tick(t, s, now)
	}
	var beats []proto.Heartbeat
	for _, m := range pub.msgs {
		if m.topic == proto.HeartbeatTopic("detector") {
			var hb proto.Heartbeat
			if err := json.Unmarshal(m.payload, &hb); err != nil {
				t.Fatal(err)
			}
			beats = append(beats, hb)
		}
	}
	if len(beats) != 5 {
		t.Fatalf("heartbeats = %d, want one per tick (5)", len(beats))
	}
	for i := 1; i < len(beats); i++ {
		if beats[i].Seq <= beats[i-1].Seq || beats[i].SimTime <= beats[i-1].SimTime {
			t.Fatalf("heartbeat did not advance: %+v", beats)
		}
	}
}

func TestHeartbeatIsSentEvenWithNoRoomsYet(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	tick(t, s, 0)
	if len(pub.msgs) != 1 || pub.msgs[0].topic != proto.HeartbeatTopic("detector") {
		t.Fatalf("msgs = %+v, want a single heartbeat", pub.msgs)
	}
}

func operatorPost(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec.Code
}

func TestOperatorEndpointConfirmsAWarningAndCommandsTheSprinkler(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	feed(t, s, roomA, 0, 0.5, 3, 21) // a warning
	tick(t, s, 0)
	h := s.OperatorHandler()
	if code := operatorPost(t, h, "/operator/level0/A109/confirm"); code != http.StatusNoContent {
		t.Fatalf("confirm = %d", code)
	}
	if cmds := pub.commands(t, roomA); len(cmds) != 1 || cmds[0].Action != proto.ActionOn {
		t.Fatalf("commands = %+v", cmds)
	}
}

func TestOperatorEndpointCancelAndRelease(t *testing.T) {
	s := newSvc(&capture{})
	feed(t, s, roomA, 0, 0.5, 3, 21)
	tick(t, s, 0)
	h := s.OperatorHandler()
	if code := operatorPost(t, h, "/operator/level0/A109/cancel"); code != http.StatusNoContent {
		t.Fatalf("cancel = %d", code)
	}
	if got := s.Rooms()[0].State; got != "cancelled" {
		t.Fatalf("state after cancel = %q", got)
	}
	if code := operatorPost(t, h, "/operator/level0/A109/release"); code != http.StatusNoContent {
		t.Fatalf("release = %d", code)
	}
}

func TestOperatorEndpointRejectsUnknownRoomAndAction(t *testing.T) {
	h := newSvc(&capture{}).OperatorHandler()
	if code := operatorPost(t, h, "/operator/level9/none/confirm"); code != http.StatusNotFound {
		t.Errorf("unknown room = %d, want 404", code)
	}
	if code := operatorPost(t, h, "/operator/level0/A109/explode"); code != http.StatusBadRequest {
		t.Errorf("unknown action = %d, want 400", code)
	}
}

func TestStatusEndpointListsRooms(t *testing.T) {
	s := newSvc(&capture{})
	calm(t, s, roomA, 0)
	tick(t, s, 0)
	rec := httptest.NewRecorder()
	s.OperatorHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var got []RoomStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(got) != 1 || got[0].Room != roomA {
		t.Fatalf("status = %d %+v", rec.Code, got)
	}
}

func TestCurrentStateIsReannouncedPeriodicallyAsARefresh(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub) // DefaultConfig: RefreshEvery 30 s
	for now := 0.0; now <= 30; now += 5 {
		fire(t, s, roomA, now)
		calm(t, s, roomB, now)
		tick(t, s, now)
	}
	before := len(pub.decisions(t, roomA))
	for now := 35.0; now <= 70; now += 5 {
		fire(t, s, roomA, now)
		calm(t, s, roomB, now)
		tick(t, s, now)
	}
	var refreshA, refreshB []Decision
	for _, d := range pub.decisions(t, roomA)[before:] {
		if d.Refresh {
			refreshA = append(refreshA, d)
		}
	}
	for _, d := range pub.decisions(t, roomB) {
		if d.Refresh {
			refreshB = append(refreshB, d)
		}
	}
	if len(refreshA) != 1 || refreshA[0].State != "fire" || refreshA[0].Action != "none" {
		t.Fatalf("refreshes for the burning room = %+v, want one per 30 s carrying state fire", refreshA)
	}
	if len(refreshB) == 0 || refreshB[0].State != "idle" {
		t.Fatalf("refreshes for the quiet room = %+v: an idle state must be re-announced too, or a missed all-clear is never repaired", refreshB)
	}
}

func TestARealDecisionIsNeverMarkedAsARefresh(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 20; now += 5 {
		fire(t, s, roomA, now)
		tick(t, s, now)
	}
	for _, d := range pub.decisions(t, roomA) {
		if d.Action == "actuate" && d.Refresh {
			t.Fatalf("the actuation was marked as a refresh: %+v", d)
		}
	}
}

func TestNoPreAlertWhileARoomsSensorsAreStillStartingUp(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	// only smoke has reported so far; CO and temperature arrive a few seconds later
	r, _ := json.Marshal(gateway.Reading{SensorID: "s", Room: roomA, Type: "smoke", Value: 0, SimTime: 1, Seq: 1})
	_ = s.Handle(context.Background(), "sensors/x", r)
	tick(t, s, 5)
	for _, d := range pub.decisions(t, roomA) {
		if d.State == "warning" {
			t.Fatalf("startup raised a pre-alert: %+v", d)
		}
	}
}

func TestASignalThatNeverArrivesStillWarnsAfterTheGracePeriod(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 40; now += 5 { // temperature never reports
		for _, r := range []gateway.Reading{
			{SensorID: "s", Room: roomA, Type: "smoke", Value: 0, SimTime: now, Seq: uint64(now) + 1},
			{SensorID: "c", Room: roomA, Type: "co", Value: 3, SimTime: now, Seq: uint64(now) + 1},
		} {
			raw, _ := json.Marshal(r)
			_ = s.Handle(context.Background(), "sensors/x", raw)
		}
		tick(t, s, now)
	}
	var warned bool
	for _, d := range pub.decisions(t, roomA) {
		if d.State == "warning" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("a dead sensor was hidden by the startup grace period")
	}
}

func TestRoomsReportsStatusForTheDashboard(t *testing.T) {
	s := newSvc(&capture{})
	for now := 0.0; now <= 30; now += 5 {
		fire(t, s, roomA, now)
		tick(t, s, now)
	}
	rooms := s.Rooms()
	if len(rooms) != 1 || rooms[0].Room != roomA || rooms[0].State != "fire" {
		t.Fatalf("rooms = %+v", rooms)
	}
}
