// Package doors decides whether each room's egress door is locked. The rooms'
// doors are locked by default (a secured building); a room in alarm has its
// door unlocked, which also puts the door back into BuildSim's evacuation
// routes, because a locked door removes its entry node from them.
//
// The rules follow fire-safety practice for access-controlled egress doors
// (decision D19): release on a fire alarm without delay, and fail safe, which
// means unlocked, whenever the control is lost, starting, or stopping.
package doors

import (
	"fmt"
	"sync"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// Lock is a door's lock state, as BuildSim stores it.
type Lock string

const (
	Locked   Lock = "locked"
	Unlocked Lock = "unlocked"
)

// Door is one controlled door: the room it leads out of and the entry node of
// BuildSim's walkable graph that the lock removes from routes.
type Door struct {
	Room      string // "<level>/<room>"
	EntryNode int
}

// Config holds the doors and the timings.
type Config struct {
	Doors []Door
	// Silence is how long the detector may be unheard (wall clock) before every
	// door is unlocked. It is the actuator's 30 sim s heartbeat limit expressed
	// in wall time: 30 / simulation speed.
	Silence time.Duration
	// QuietFor is how many sim seconds the detector must have been heard, with
	// no alarm, before the doors are locked again after a start or a clock reset.
	QuietFor float64
}

// Controller holds the state behind Desired. Safe for concurrent use.
type Controller struct {
	cfg   Config
	clock func() time.Time

	mu          sync.Mutex
	started     time.Time
	heard       bool
	heardAt     time.Time
	detSim      float64 // the detector's latest sim time
	firstSim    float64 // its sim time when it was first heard, or after a reset
	unlockUntil map[string]float64
	released    bool // shutdown: everything stays unlocked
}

// New returns a controller. clock is time.Now in production.
func New(cfg Config, clock func() time.Time) *Controller {
	return &Controller{cfg: cfg, clock: clock, started: clock(), unlockUntil: map[string]float64{}}
}

func (c *Controller) known(room string) bool {
	for _, d := range c.cfg.Doors {
		if d.Room == room {
			return true
		}
	}
	return false
}

// OnHeartbeat records a heartbeat. Only the detector's counts.
func (c *Controller) OnHeartbeat(hb proto.Heartbeat) {
	if hb.Process != "detector" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.heardAt = c.clock()
	switch {
	case !c.heard:
		c.heard, c.firstSim = true, hb.SimTime
	case hb.SimTime < c.detSim-1:
		// the simulation clock went back (physics-sim restarted): deadlines from
		// the old time frame mean nothing, so start the quiet period again
		c.firstSim = hb.SimTime
		c.unlockUntil = map[string]float64{}
	}
	c.detSim = hb.SimTime
}

// OnCommand applies an unlock command. A malformed command, one for a room
// without a door, or one that has already expired changes nothing.
func (c *Controller) OnCommand(cmd proto.DoorCommand) error {
	if err := cmd.Validate(); err != nil {
		return err
	}
	if !c.known(cmd.Room) {
		return fmt.Errorf("doors: no door in %s", cmd.Room)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cmd.Expired(c.detSim) {
		return nil
	}
	if until := cmd.SimTime + cmd.TTL; until > c.unlockUntil[cmd.Room] {
		c.unlockUntil[cmd.Room] = until
	}
	return nil
}

// ReleaseAll unlocks every door for good: the actuator is shutting down, and a
// real fail-safe lock would release when its power goes.
func (c *Controller) ReleaseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.released = true
}

// Desired is the lock state each door should be in now, by room.
func (c *Controller) Desired() map[string]Lock {
	c.mu.Lock()
	defer c.mu.Unlock()
	failSafe := c.released || !c.heard || // never heard the detector: cannot know there is no fire
		c.detSim-c.firstSim < c.cfg.QuietFor || // heard, but not yet quiet for long enough
		c.clock().Sub(c.heardAt) > c.cfg.Silence // the detector went quiet: control lost
	out := make(map[string]Lock, len(c.cfg.Doors))
	for _, d := range c.cfg.Doors {
		if failSafe || c.detSim < c.unlockUntil[d.Room] {
			out[d.Room] = Unlocked
		} else {
			out[d.Room] = Locked
		}
	}
	return out
}

// Doors lists the controlled doors.
func (c *Controller) Doors() []Door { return append([]Door(nil), c.cfg.Doors...) }
