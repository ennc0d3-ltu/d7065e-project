package responder

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// OccupancySource reads who is where; *buildsim.Client implements it.
type OccupancySource interface {
	GetOccupancy(ctx context.Context) (map[string]buildsim.RoomOccupancy, error)
}

// EntitySource reads who is walking; *buildsim.Client implements it. It is
// optional: without it the responder knows only who is in which room.
type EntitySource interface {
	GetEntities(ctx context.Context) ([]buildsim.Entity, error)
}

// Publisher sends one message to the broker.
type Publisher interface {
	Publish(ctx context.Context, topic string, qos byte, payload []byte) error
}

// Config holds the building knowledge and timings (sim seconds).
type Config struct {
	Safe        []string            // assembly rooms to route to ("<level>/<room>")
	Adjacent    map[string][]string // rooms that share a door
	MinInterval float64             // least time between non-urgent updates
	AckDelay    float64             // how long the mock responder takes to acknowledge
}

type pendingAck struct {
	incident string
	due      float64
}

// Service builds dispatches from the detector's decisions. Safe for
// concurrent use.
type Service struct {
	cfg   Config
	occ   OccupancySource
	rf    RouteFinder
	pub   Publisher
	ents  EntitySource // optional
	watch *Watch

	mu       sync.Mutex
	affected map[string]bool

	occLast  map[string]buildsim.RoomOccupancy
	occKnown bool
	occStale bool

	incident     string
	incidentSeq  int
	opened       bool
	routesFor    string  // affected-set key the cached routes were computed for
	routesTried  float64 // sim time of the last route computation
	routes       map[string]RouteChoice
	noRoute      []string
	lastSent     float64
	lastDanger   int
	lastRisk     int
	lastMove     [2]int // en route, unaccounted
	lastAffected []string
	pending      *proto.Dispatch // built but not yet published
	acks         []pendingAck
	acked        []proto.DispatchAck
}

// WithEntities lets the responder see who is walking out of an affected room.
func (s *Service) WithEntities(e EntitySource) { s.ents = e }

// routeRetryEvery is how often (sim seconds) a room with no route is looked up
// again while its affected set stays the same.
const routeRetryEvery = 5

// New returns a responder service.
func New(cfg Config, occ OccupancySource, rf RouteFinder, pub Publisher) *Service {
	return &Service{cfg: cfg, occ: occ, rf: rf, pub: pub, affected: map[string]bool{}, routes: map[string]RouteChoice{}, watch: NewWatch()}
}

// HandleDecision records a detector decision. Only the fire state affects a room.
func (s *Service) HandleDecision(_ context.Context, _ string, payload []byte) error {
	var d proto.Decision
	if err := json.Unmarshal(payload, &d); err != nil {
		return fmt.Errorf("responder: bad decision: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.State == "fire" {
		s.affected[d.Room] = true
	} else {
		delete(s.affected, d.Room)
	}
	return nil
}

// Acks returns the acknowledgements received so far.
func (s *Service) Acks() []proto.DispatchAck {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.acked)
}

func (s *Service) affectedList() []string {
	out := make([]string, 0, len(s.affected))
	for r := range s.affected {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// Tick advances the service to sim time now: refresh occupancy, open or close
// an incident, recompute routes when the affected set changed, and publish a
// dispatch when something worth telling responders happened.
func (s *Service) Tick(ctx context.Context, now float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if occ, err := s.occ.GetOccupancy(ctx); err == nil {
		s.occLast, s.occKnown, s.occStale = occ, true, false
	} else {
		s.occStale = true // keep the last known picture, flagged
	}

	s.flushAcks(ctx, now)

	affected := s.affectedList()
	switch {
	case len(affected) == 0 && s.opened:
		s.closeIncident(ctx, now)
		return
	case len(affected) == 0:
		return
	}

	grew, found := s.updateRoutes(ctx, now, affected)
	c := Classify(s.occLast, s.affected, s.cfg.Adjacent)
	if s.ents != nil {
		ents, err := s.ents.GetEntities(ctx)
		if err != nil {
			ents = nil // the room picture still stands; walkers show as unaccounted
		}
		mv := s.watch.Update(s.occLast, ents, s.affected)
		c.InDanger, c.EnRoute, c.Unaccounted = mv.InDanger, mv.EnRoute, mv.Unaccounted
	}

	if s.pending == nil {
		kind := ""
		switch {
		case !s.opened:
			s.opened = true
			s.incidentSeq++
			s.incident = fmt.Sprintf("inc-%d-t%.0f", s.incidentSeq, now)
			kind = "fire"
		case grew || found:
			kind = "update" // a spreading fire, or a route that has appeared, is never rate-limited
		case (!slices.Equal(affected, s.lastAffected) || len(c.InDanger) != s.lastDanger || len(c.AtRisk) != s.lastRisk ||
			[2]int{len(c.EnRoute), len(c.Unaccounted)} != s.lastMove) &&
			now-s.lastSent >= s.cfg.MinInterval:
			kind = "update"
		}
		if kind != "" {
			s.pending = s.build(kind, "critical", now, affected, c)
		}
	}
	s.sendPending(ctx, now, affected, c)
}

// updateRoutes recomputes routes only when the affected set changed (D11) and
// reports whether it grew. A room whose routing fails keeps its last good
// route if that route still avoids every affected room.
func (s *Service) updateRoutes(ctx context.Context, now float64, affected []string) (grew, found bool) {
	key := strings.Join(affected, ",")
	if key == s.routesFor {
		// Nothing changed, but a room that had no route may have one now: a
		// locked door that the door actuator has since unlocked, say. Ask again,
		// not on every tick.
		if len(s.noRoute) == 0 || now-s.routesTried < routeRetryEvery {
			return false, false
		}
	}
	s.routesTried = now
	missing := len(s.noRoute)
	for _, r := range affected {
		if !slices.Contains(s.lastAffected, r) {
			grew = s.opened || grew
		}
	}
	fresh := map[string]RouteChoice{}
	var none []string
	for _, room := range affected {
		choice, err := PickRoute(ctx, s.rf, room, s.affected, s.cfg.Safe)
		if err != nil {
			if old, ok := s.routes[room]; ok && !crossesAffected(buildsim.Route{Path: old.Path}, room, s.affected) {
				fresh[room] = old
				continue
			}
			none = append(none, room)
			continue
		}
		fresh[room] = choice
	}
	s.routes, s.noRoute, s.routesFor = fresh, none, key
	return grew, len(none) < missing // a route that was missing has been found
}

func refs(os []Occupant) []proto.PersonRef {
	out := make([]proto.PersonRef, 0, len(os))
	for _, o := range os {
		out = append(out, proto.PersonRef{ID: o.ID, Name: o.Name, Room: o.Room})
	}
	return out
}

func (s *Service) build(kind, severity string, now float64, affected []string, c Classification) *proto.Dispatch {
	d := &proto.Dispatch{
		Incident: s.incident, Kind: kind, Severity: severity, SimTime: now,
		Affected: affected, InDanger: refs(c.InDanger), AtRisk: refs(c.AtRisk), Safe: c.Safe,
		EnRoute: refs(c.EnRoute), Unaccounted: refs(c.Unaccounted),
		Routes: []proto.RouteMsg{}, NoRoute: slices.Clone(s.noRoute),
		OccupancyStale: s.occStale, OccupancyUnknown: !s.occKnown,
	}
	for _, room := range affected {
		if r, ok := s.routes[room]; ok {
			d.Routes = append(d.Routes, proto.RouteMsg{
				Incident: s.incident, From: r.From, To: r.To, Rooms: r.Rooms(), Distance: r.Distance, SimTime: now,
			})
		}
	}
	return d
}

// sendPending publishes the pending dispatch and its routes. On failure it
// stays pending and is retried on the next tick.
func (s *Service) sendPending(ctx context.Context, now float64, affected []string, c Classification) {
	if s.pending == nil {
		return
	}
	d := s.pending
	payload, err := json.Marshal(d)
	if err != nil || s.pub.Publish(ctx, proto.DispatchTopic, 1, payload) != nil {
		return
	}
	for _, r := range d.Routes {
		if p, err := json.Marshal(r); err == nil {
			_ = s.pub.Publish(ctx, proto.RouteTopic(r.From), 1, p) // the dashboard re-reads on the next update
		}
	}
	s.lastSent, s.lastAffected = now, slices.Clone(affected)
	s.lastDanger, s.lastRisk = len(c.InDanger), len(c.AtRisk)
	s.lastMove = [2]int{len(c.EnRoute), len(c.Unaccounted)}
	s.acks = append(s.acks, pendingAck{incident: d.Incident, due: now + s.cfg.AckDelay})
	s.pending = nil
}

func (s *Service) closeIncident(ctx context.Context, now float64) {
	if s.pending == nil || s.pending.Kind != "all-clear" {
		s.pending = s.build("all-clear", "info", now, nil, Classification{})
	}
	payload, err := json.Marshal(s.pending)
	if err != nil || s.pub.Publish(ctx, proto.DispatchTopic, 1, payload) != nil {
		return // retried on the next tick
	}
	s.acks = append(s.acks, pendingAck{incident: s.pending.Incident, due: now + s.cfg.AckDelay})
	s.pending, s.opened = nil, false
	s.routes, s.noRoute, s.routesFor, s.lastAffected = map[string]RouteChoice{}, nil, "", nil
	s.lastDanger, s.lastRisk, s.lastMove = 0, 0, [2]int{}
	s.watch.Reset()
}

// flushAcks plays the mock responder: each dispatch is acknowledged once,
// AckDelay sim seconds after it went out, and the acknowledgement is logged.
func (s *Service) flushAcks(ctx context.Context, now float64) {
	keep := s.acks[:0]
	for _, a := range s.acks {
		if now < a.due {
			keep = append(keep, a)
			continue
		}
		ack := proto.DispatchAck{Incident: a.incident, Responder: "mock-responder", SimTime: now}
		if p, err := json.Marshal(ack); err == nil {
			_ = s.pub.Publish(ctx, proto.DispatchAckTopic, 1, p)
		}
		s.acked = append(s.acked, ack)
	}
	s.acks = keep
}
