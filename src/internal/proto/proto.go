// Package proto holds the messages and topics shared between processes, so
// the detector and the sprinkler actuator cannot disagree on a field name
// (report §5: interfaces precise enough to reimplement a component).
package proto

import (
	"errors"
	"strings"
)

// Command actions for a sprinkler.
const (
	ActionOn  = "on"
	ActionOff = "off"
)

// Command is the detector's order to one room's sprinkler. It is idempotent
// (the same command twice has the same effect) and carries a TTL in sim
// seconds: the detector re-sends it while the response is active, and an
// actuator that stops hearing it falls back to local rules (D6).
type Command struct {
	Room    string  `json:"room"`   // "<level>/<room>"
	Action  string  `json:"action"` // ActionOn or ActionOff
	Reason  string  `json:"reason"` // human-readable, for the audit trail
	Seq     uint64  `json:"seq"`    // per-room counter; duplicates are harmless
	TTL     float64 `json:"ttl_s"`  // validity in sim seconds from SimTime
	SimTime float64 `json:"sim_time"`
}

// Validate rejects a malformed command before it can reach an actuator.
func (c Command) Validate() error {
	switch {
	case c.Room == "":
		return errors.New("proto: command has no room")
	case c.Action != ActionOn && c.Action != ActionOff:
		return errors.New("proto: command action must be on or off")
	case c.Seq == 0:
		return errors.New("proto: command has no sequence number")
	case c.TTL <= 0:
		return errors.New("proto: command TTL must be positive")
	}
	return nil
}

// Expired reports whether the command has outlived its TTL at sim time now.
func (c Command) Expired(now float64) bool { return now >= c.SimTime+c.TTL }

// DoorUnlock is the only door action: a door is locked by default (a secured
// building) and unlocked while the room is in alarm. There is no "lock" order:
// the actuator relocks by itself when the unlock command runs out, so a dead or
// silent detector can never leave a door locked behind it (fail-safe, D19).
const DoorUnlock = "unlock"

// DoorCommand is the detector's order to unlock a room's egress door while the
// room is in alarm. Like Command it is idempotent, carries a TTL in sim
// seconds and is re-sent every tick while the alarm lasts.
type DoorCommand struct {
	Room    string  `json:"room"`   // "<level>/<room>"
	Action  string  `json:"action"` // DoorUnlock
	Reason  string  `json:"reason"`
	Seq     uint64  `json:"seq"`
	TTL     float64 `json:"ttl_s"`
	SimTime float64 `json:"sim_time"`
}

// Validate rejects a malformed door command.
func (c DoorCommand) Validate() error {
	switch {
	case c.Room == "":
		return errors.New("proto: door command has no room")
	case c.Action != DoorUnlock:
		return errors.New("proto: door command action must be unlock")
	case c.Seq == 0:
		return errors.New("proto: door command has no sequence number")
	case c.TTL <= 0:
		return errors.New("proto: door command TTL must be positive")
	}
	return nil
}

// Expired reports whether the command has outlived its TTL at sim time now.
func (c DoorCommand) Expired(now float64) bool { return now >= c.SimTime+c.TTL }

const doorPrefix = "commands/door/"

// DoorTopic is where the detector publishes a room's door command.
func DoorTopic(room string) string { return doorPrefix + room }

// RoomFromDoorTopic recovers the room key from a door command topic.
func RoomFromDoorTopic(topic string) (string, bool) {
	room, ok := strings.CutPrefix(topic, doorPrefix)
	if !ok || room == "" {
		return "", false
	}
	return room, true
}

// Decision is published on decisions/<room> whenever a room's state changes or
// an action is taken. The reason string makes every action auditable after the
// fact (NFR5).
type Decision struct {
	Room    string   `json:"room"`
	SimTime float64  `json:"sim_time"`
	Tier    string   `json:"tier"`
	State   string   `json:"state"`
	Action  string   `json:"action"`
	Reason  string   `json:"reason"`
	Stale   []string `json:"stale,omitempty"`
	// Refresh marks a periodic re-announcement of the current state, so a
	// consumer that missed a change (broker outage, restart) catches up. It
	// is not a new decision: the audit trail does not store it.
	Refresh bool `json:"refresh,omitempty"`
}

// Heartbeat is a process's liveness message. The detector sends one on every
// tick, even when nothing is wrong, so an actuator can tell "detector dead"
// from "detector alive and quiet" (D6).
type Heartbeat struct {
	Process string  `json:"process"`
	Seq     uint64  `json:"seq"`
	SimTime float64 `json:"sim_time"`
}

// Validate rejects a malformed heartbeat.
func (h Heartbeat) Validate() error {
	if h.Process == "" {
		return errors.New("proto: heartbeat has no process name")
	}
	return nil
}

const commandPrefix = "commands/sprinkler/"

// CommandTopic is where the detector publishes a room's sprinkler command.
func CommandTopic(room string) string { return commandPrefix + room }

// DecisionTopic is where the detector publishes a room's decisions.
func DecisionTopic(room string) string { return "decisions/" + room }

// HeartbeatTopic is where a process publishes its liveness.
func HeartbeatTopic(process string) string { return "heartbeat/" + process }

// RoomFromCommandTopic recovers the room key from a command topic.
func RoomFromCommandTopic(topic string) (string, bool) {
	room, ok := strings.CutPrefix(topic, commandPrefix)
	if !ok || room == "" {
		return "", false
	}
	return room, true
}

// Topics for the responder's output.
const (
	DispatchTopic    = "dispatch/responders" // what responders are told
	DispatchAckTopic = "dispatch/ack"        // their acknowledgement
)

// RouteTopic is where the responder publishes a room's evacuation route, for
// the dashboard to draw (the dashboard owns BuildSim's overlays).
func RouteTopic(room string) string { return "route/" + room }

// PersonRef is one occupant and where they were last seen.
type PersonRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Room string `json:"room"` // "<level>/<room>"
}

// RouteMsg is an evacuation route out of an affected room to a safe room.
type RouteMsg struct {
	Incident string   `json:"incident"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Rooms    []string `json:"rooms"` // rooms passed through, in order
	Distance float64  `json:"distance"`
	SimTime  float64  `json:"sim_time"`
}

// Dispatch is what the (mock) emergency responders are told: the affected
// rooms, severity, who is in danger or at risk, and the routes out (FR6).
// Status comes from room, and, when the evacuation proxy is running, from who is walking out.
type Dispatch struct {
	Incident string  `json:"incident"`
	Kind     string  `json:"kind"`     // fire, update or all-clear
	Severity string  `json:"severity"` // critical, or info for an all-clear
	SimTime  float64 `json:"sim_time"`

	Affected []string    `json:"affected"`
	InDanger []PersonRef `json:"in_danger"`
	AtRisk   []PersonRef `json:"at_risk"`
	Safe     int         `json:"safe"`

	// EnRoute: people who were in an affected room and are walking out.
	// Unaccounted: people who were in an affected room and are now neither in a
	// room nor walking out (they may have left the building or be missing).
	EnRoute     []PersonRef `json:"en_route,omitempty"`
	Unaccounted []PersonRef `json:"unaccounted,omitempty"`

	Routes  []RouteMsg `json:"routes"`
	NoRoute []string   `json:"no_route,omitempty"` // affected rooms with no safe route

	// OccupancyStale: occupancy could not be read and the last known picture
	// was used. OccupancyUnknown: it has never been read, so the counts above
	// say nothing and must not be read as "nobody is there".
	OccupancyStale   bool `json:"occupancy_stale,omitempty"`
	OccupancyUnknown bool `json:"occupancy_unknown,omitempty"`
}

// DispatchAck is a responder's acknowledgement of a dispatch.
type DispatchAck struct {
	Incident  string  `json:"incident"`
	Responder string  `json:"responder"`
	SimTime   float64 `json:"sim_time"`
}

// DeadLetter is a message a process received but could not use (malformed,
// unknown room, ...). It is set aside on dlq/<process> for inspection instead
// of being dropped silently, and the db-writer stores it.
type DeadLetter struct {
	Process string `json:"process"`
	Topic   string `json:"topic"`   // where the bad message arrived
	Payload string `json:"payload"` // as received, truncated
	Error   string `json:"error"`
	WallTS  string `json:"wall_ts"` // RFC 3339
}

// DeadLetterTopic is where a process sets aside messages it could not use.
func DeadLetterTopic(process string) string { return "dlq/" + process }
