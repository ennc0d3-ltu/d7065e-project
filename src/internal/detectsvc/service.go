// Package detectsvc is the detector process's logic: readings in, per-room
// features and escalation, decisions and sprinkler commands out. It talks to
// the broker only through the Publisher interface, so it is testable without
// one.
package detectsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// ErrUnknownRoom is returned for an operator action on a room with no readings.
var ErrUnknownRoom = errors.New("detectsvc: unknown room")

// Publisher sends one message to the broker.
type Publisher interface {
	Publish(ctx context.Context, topic string, qos byte, payload []byte) error
}

// Config holds the service settings (sim seconds).
type Config struct {
	Escalation detector.Config
	CommandTTL float64 // validity of each sprinkler command
	Horizon    float64 // feature window length
	MaxAge     float64 // a signal older than this is stale
	// RefreshEvery re-announces each room's current state this often (sim s),
	// so a consumer that missed a decision during a broker outage catches up.
	RefreshEvery float64
}

// DefaultConfig returns the starting values, to be tuned (open item).
func DefaultConfig() Config {
	return Config{Escalation: detector.DefaultConfig(), CommandTTL: 30, Horizon: 60, MaxAge: 15, RefreshEvery: 30}
}

// Decision is the message published on decisions/<room> (defined in proto so
// every consumer shares it).
type Decision = proto.Decision

// RoomStatus is one room's current state, for the dashboard.
type RoomStatus struct {
	Room    string
	State   string
	Tier    string
	SimTime float64
}

type room struct {
	feat    *detector.RoomFeatures
	esc     *detector.Escalator
	seq     uint64
	doorSeq uint64 // door commands have their own counter
	state   detector.State
	tier    detector.Tier
	simTime float64
	// pendingOff is set when an off command could not be published: it is
	// retried every tick until it gets through.
	pendingOff bool
	// announcedAt is when the room's state was last published (a decision
	// or a refresh).
	announcedAt float64
	// firstSeen is the sim time of the room's first reading. For MaxAge after
	// it, signals that have not reported yet are starting up, not stale, so
	// booting the system does not raise a pre-alert in every room.
	firstSeen float64
}

// Service decides, per room, whether to respond to a fire.
type Service struct {
	cfg      Config
	deployed atomic.Pointer[classifier] // rule floor + model + heat gate; swapped by Apply
	pub      Publisher

	mu     sync.Mutex
	rooms  map[string]*room
	simNow float64
	hbSeq  uint64
}

// New returns a service. A nil model makes the rule baseline the detector.
func New(cfg Config, model *detector.Model, th detector.Thresholds, pub Publisher) *Service {
	s := &Service{cfg: cfg, pub: pub, rooms: map[string]*room{}}
	s.Apply(th, model)
	return s
}

type classifier func(vec []float64) detector.Tier

// Apply replaces the thresholds and model the detector decides with, without a
// restart and without disturbing any room's state. The caller validates first
// (see Loader): Apply trusts what it is given.
func (s *Service) Apply(th detector.Thresholds, model *detector.Model) {
	c := classifier(detector.Deployed(th, model))
	s.deployed.Store(&c)
}

func (s *Service) roomFor(key string) *room {
	r, ok := s.rooms[key]
	if !ok {
		r = &room{
			feat: detector.NewRoomFeatures(s.cfg.Horizon, s.cfg.MaxAge),
			esc:  detector.NewEscalator(s.cfg.Escalation),
		}
		s.rooms[key] = r
	}
	return r
}

// Handle records one sensor reading (an MQTT message on sensors/#).
func (s *Service) Handle(_ context.Context, _ string, payload []byte) error {
	var rd gateway.Reading
	if err := json.Unmarshal(payload, &rd); err != nil {
		return fmt.Errorf("detectsvc: bad reading: %w", err)
	}
	if rd.Room == "" {
		return errors.New("detectsvc: reading has no room")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, known := s.rooms[rd.Room]
	if !known {
		r = s.roomFor(rd.Room)
		r.firstSeen = rd.SimTime
	}
	r.feat.Add(rd)
	s.simNow = max(s.simNow, rd.SimTime)
	return nil
}

// SimNow is the latest sim time seen in any reading.
func (s *Service) SimNow() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.simNow
}

// Tick evaluates every room at sim time now: classify, escalate, publish.
func (s *Service) Tick(ctx context.Context, now float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.rooms))
	for k := range s.rooms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		r := s.rooms[key]
		vec, stale := r.feat.VectorDegraded(now)
		if len(stale) > 0 && now-r.firstSeen < s.cfg.MaxAge {
			continue // still starting up: a 15 s blind spot per room at boot, by design
		}
		tier := s.classify(vec)
		d := r.esc.Update(now, tier)
		s.apply(ctx, key, r, now, tier, stale, d)
	}
	// Last, so a broker hiccup that drops the response messages is not
	// hidden behind a heartbeat that looks healthy.
	s.hbSeq++
	s.publishJSON(ctx, proto.HeartbeatTopic("detector"), proto.Heartbeat{
		Process: "detector", Seq: s.hbSeq, SimTime: now,
	})
}

// classify returns the tier for a feature vector. No vector means every
// signal is stale: never normal. See detector.Deployed for the rule floor and
// the heat gate.
func (s *Service) classify(vec []float64) detector.Tier {
	if vec == nil {
		return detector.TierWarning
	}
	return (*s.deployed.Load())(vec)
}

// apply publishes what a decision calls for. Commands are re-sent every tick
// while the response is latched, so the actuator's TTL is refreshed and a lost
// message is repaired on the next tick (idempotent: the actuator just stays on).
func (s *Service) apply(ctx context.Context, key string, r *room, now float64, tier detector.Tier, stale []string, d detector.Decision) {
	changed := d.State != r.state
	r.state, r.tier, r.simTime = d.State, tier, now

	switch {
	case d.Action != detector.ActionNone || changed:
		s.publishJSON(ctx, proto.DecisionTopic(key), Decision{
			Room: key, SimTime: now, Tier: tier.String(), State: d.State.String(),
			Action: d.Action.String(), Reason: d.Reason, Stale: stale,
		})
		r.announcedAt = now
	case s.cfg.RefreshEvery > 0 && now-r.announcedAt >= s.cfg.RefreshEvery:
		s.publishJSON(ctx, proto.DecisionTopic(key), Decision{
			Room: key, SimTime: now, Tier: tier.String(), State: d.State.String(),
			Action: detector.ActionNone.String(), Reason: "state refresh", Stale: stale, Refresh: true,
		})
		r.announcedAt = now
	}

	switch {
	case d.State == detector.StateFire:
		reason := d.Reason
		if d.Action != detector.ActionActuate {
			reason = "response active"
		}
		s.command(ctx, key, r, now, proto.ActionOn, reason)
	case d.Action == detector.ActionRelease:
		r.pendingOff = true
	}
	if r.pendingOff {
		if s.command(ctx, key, r, now, proto.ActionOff, "all-clear: "+d.Reason) {
			r.pendingOff = false
		}
	}

	// A room in alarm releases its door at once (fire-safety practice: an
	// access-controlled egress door unlocks on a fire alarm, without waiting for
	// the sprinkler). The alarm is the armed or fire tier, or a latched fire
	// whose score has fallen: the door must not lock behind the people. There is
	// no "lock" order: the actuator relocks when this command stops (D19).
	switch {
	case d.State == detector.StateFire:
		s.doorCommand(ctx, key, r, now, "fire response active")
	case tier >= detector.TierArmed:
		s.doorCommand(ctx, key, r, now, "alarm: "+tier.String())
	}
}

// doorCommand publishes the unlock order for a room's door. It is sent on every
// tick while the alarm lasts, so a lost message is repaired by the next one.
func (s *Service) doorCommand(ctx context.Context, key string, r *room, now float64, reason string) {
	r.doorSeq++
	s.publishJSON(ctx, proto.DoorTopic(key), proto.DoorCommand{
		Room: key, Action: proto.DoorUnlock, Reason: reason, Seq: r.doorSeq, TTL: s.cfg.CommandTTL, SimTime: now,
	})
}

// command publishes a sprinkler command and reports whether it got through.
func (s *Service) command(ctx context.Context, key string, r *room, now float64, action, reason string) bool {
	r.seq++
	return s.publishJSON(ctx, proto.CommandTopic(key), proto.Command{
		Room: key, Action: action, Reason: reason, Seq: r.seq, TTL: s.cfg.CommandTTL, SimTime: now,
	})
}

func (s *Service) publishJSON(ctx context.Context, topic string, v any) bool {
	payload, err := json.Marshal(v)
	if err != nil {
		return false
	}
	return s.pub.Publish(ctx, topic, 1, payload) == nil
}

// operator applies an operator action and publishes its effect at once.
func (s *Service) operator(ctx context.Context, key string, act func(*detector.Escalator, float64) detector.Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[key]
	if !ok {
		return ErrUnknownRoom
	}
	d := act(r.esc, s.simNow)
	s.apply(ctx, key, r, s.simNow, r.tier, nil, d)
	return nil
}

// Confirm is the operator confirming a fire in a room.
func (s *Service) Confirm(ctx context.Context, key string) error {
	return s.operator(ctx, key, (*detector.Escalator).Confirm)
}

// Cancel is the operator cancelling a warning in a room.
func (s *Service) Cancel(ctx context.Context, key string) error {
	return s.operator(ctx, key, (*detector.Escalator).Cancel)
}

// Release is the operator ending a latched response in a room.
func (s *Service) Release(ctx context.Context, key string) error {
	return s.operator(ctx, key, (*detector.Escalator).Release)
}

// Rooms returns every room's status, sorted by name.
func (s *Service) Rooms() []RoomStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RoomStatus, 0, len(s.rooms))
	for k, r := range s.rooms {
		out = append(out, RoomStatus{Room: k, State: r.state.String(), Tier: r.tier.String(), SimTime: r.simTime})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Room < out[j].Room })
	return out
}
