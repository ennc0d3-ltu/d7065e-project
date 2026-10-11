// Package actuator is the sprinkler controller's decision logic (decision D6).
// It obeys the detector, latches on, and keeps working when the detector is
// silent by falling back to the same threshold rules the detector uses as its
// baseline. The fallback is not a model override: it only runs while the
// detector is silent.
package actuator

import (
	"fmt"
	"math"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// Source says who is driving the sprinkler.
type Source int

const (
	SourceNone     Source = iota // off
	SourceDetector               // a command from the detector
	SourceFallback               // local rules, detector silent
)

func (s Source) String() string { return [...]string{"none", "detector", "fallback"}[s] }

// Config holds the actuator's timings (sim seconds) and shared thresholds.
type Config struct {
	FirePersist  float64 // fallback: fire tier must hold this long
	ClearHold    float64 // local all-clear: quiet for this long releases the latch
	HeartbeatTTL float64 // the detector counts as silent after this long unheard
	Horizon      float64 // feature window
	MaxAge       float64 // a signal older than this is stale
	// Thresholds must come from the same source as the detector's baseline
	// (one shared config, so the two cannot drift apart).
	Thresholds detector.Thresholds
}

// DefaultConfig returns the starting values, to be tuned.
func DefaultConfig() Config {
	return Config{
		FirePersist: 15, ClearHold: 300, HeartbeatTTL: 30, Horizon: 60, MaxAge: 15,
		Thresholds: detector.DefaultThresholds(),
	}
}

// Output is the state the sprinkler should be in.
type Output struct {
	On     bool
	Source Source
	Reason string
}

// Controller drives one room's sprinkler. It is not safe for concurrent use;
// the actuator process owns one per room.
type Controller struct {
	room string
	cfg  Config
	feat *detector.RoomFeatures

	on     bool
	source Source
	reason string

	latest    float64 // newest sim time observed anywhere
	firstSeen float64 // first sim time observed, or NaN
	heardAt   float64 // newest sim time the detector was heard, or NaN

	cmdSeen    bool
	lastCmdSim float64
	lastCmdSeq uint64

	fireSince  float64 // fallback tier tracking, -1 when not holding
	quietSince float64
}

// New returns a controller for one room, starting off.
func New(room string, cfg Config) *Controller {
	return &Controller{
		room: room, cfg: cfg,
		feat:      detector.NewRoomFeatures(cfg.Horizon, cfg.MaxAge),
		firstSeen: math.NaN(), heardAt: math.NaN(),
		fireSince: -1, quietSince: -1,
	}
}

// Restore adopts the sprinkler state BuildSim already holds, read once at
// startup. BuildSim keeps actuator state across a restart of this process, so
// without it a restart in the middle of a fire would switch the sprinkler
// off. A restored "on" is released by the detector's off command or by a local
// all-clear, and any new on command from the detector takes it over.
func (c *Controller) Restore(on bool) {
	if !on {
		return
	}
	c.on, c.source = true, SourceFallback
	c.reason = "restored after restart: sprinkler was already on"
}

func (c *Controller) observe(t float64) {
	if math.IsNaN(c.firstSeen) {
		c.firstSeen = t
	}
	c.latest = max(c.latest, t)
}

func (c *Controller) heard(t float64) {
	if math.IsNaN(c.heardAt) || t > c.heardAt {
		c.heardAt = t
	}
}

// OnReading feeds a sensor reading for the local fallback rules.
func (c *Controller) OnReading(r gateway.Reading) {
	if r.Room != c.room {
		return
	}
	c.observe(r.SimTime)
	c.feat.Add(r)
}

// OnHeartbeat records that the detector is alive.
func (c *Controller) OnHeartbeat(hb proto.Heartbeat) {
	if hb.Validate() != nil {
		return
	}
	c.observe(hb.SimTime)
	c.heard(hb.SimTime)
}

// OnCommand applies a detector command and reports whether it was accepted.
// A command is dropped if it is malformed, for another room, expired, or not
// newer than the last accepted one (sim time first, then sequence, so a
// restarted detector whose counter reset is still obeyed).
func (c *Controller) OnCommand(cmd proto.Command) bool {
	if cmd.Validate() != nil || cmd.Room != c.room {
		return false
	}
	c.observe(cmd.SimTime)
	if cmd.Expired(c.latest) {
		return false
	}
	if c.cmdSeen && (cmd.SimTime < c.lastCmdSim || (cmd.SimTime == c.lastCmdSim && cmd.Seq <= c.lastCmdSeq)) {
		return false
	}
	c.cmdSeen, c.lastCmdSim, c.lastCmdSeq = true, cmd.SimTime, cmd.Seq
	c.heard(cmd.SimTime)
	if cmd.Action == proto.ActionOn {
		c.on, c.source, c.reason = true, SourceDetector, cmd.Reason
	} else {
		c.on, c.source, c.reason = false, SourceNone, cmd.Reason
	}
	return true
}

// detectorLost reports whether the detector has been silent longer than the
// TTL. A detector never heard at all counts as silent once the TTL has
// passed since the first thing this controller observed.
func (c *Controller) detectorLost(now float64) bool {
	if math.IsNaN(c.heardAt) {
		return !math.IsNaN(c.firstSeen) && now-c.firstSeen > c.cfg.HeartbeatTTL
	}
	return now-c.heardAt > c.cfg.HeartbeatTTL
}

// Step evaluates the controller at sim time now and returns the state the
// sprinkler should be in.
func (c *Controller) Step(now float64) Output {
	c.observe(now)

	// local view of the air, with the same rules as the detector's baseline
	vec, _ := c.feat.VectorDegraded(now)
	tier := detector.TierWarning // no data is never normal
	if vec != nil {
		tier = c.cfg.Thresholds.Classify(vec)
	}
	if tier == detector.TierFire {
		if c.fireSince < 0 {
			c.fireSince = now
		}
	} else {
		c.fireSince = -1
	}
	if tier == detector.TierNone {
		if c.quietSince < 0 {
			c.quietSince = now
		}
	} else {
		c.quietSince = -1
	}
	quiet := c.quietSince >= 0 && now-c.quietSince >= c.cfg.ClearHold
	lost := c.detectorLost(now)

	switch {
	case !c.on:
		if lost && c.fireSince >= 0 && now-c.fireSince >= c.cfg.FirePersist {
			c.on, c.source = true, SourceFallback
			c.reason = fmt.Sprintf("local fallback: fire tier held %.0fs while the detector was silent", now-c.fireSince)
		}
	case c.source == SourceDetector && lost && quiet:
		c.on, c.source = false, SourceNone
		c.reason = "detector silent; local all-clear"
	case c.source == SourceFallback && quiet:
		c.on, c.source = false, SourceNone
		c.reason = "local all-clear after fallback activation"
	}
	return Output{On: c.on, Source: c.source, Reason: c.reason}
}
