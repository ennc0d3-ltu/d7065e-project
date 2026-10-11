package detector

import "fmt"

// Mode is the escalation policy (decision D4).
type Mode int

const (
	// ModePersistence: a tier that persists escalates by itself; the operator
	// has no say.
	ModePersistence Mode = iota
	// ModeOperator: a persisting tier waits for the operator, and falls back
	// to persistence after OperatorTimeout, so the human can speed or cancel
	// the response but is never required.
	ModeOperator
	// ModeBoth (default): persistence escalates at once, and the operator may
	// also confirm earlier or cancel.
	ModeBoth
)

// State is where one room is in its response.
type State int

const (
	StateIdle      State = iota // nothing wrong
	StateWarning                // pre-alert raised, watching
	StateFire                   // response active; latched until all-clear (FR7)
	StateCancelled              // operator cancelled; suppressed for CancelHold
)

func (s State) String() string {
	return [...]string{"idle", "warning", "fire", "cancelled"}[s]
}

// Action is what the caller must do for this decision.
type Action int

const (
	ActionNone    Action = iota
	ActionWarn           // raise the pre-alert (dashboard, responders)
	ActionActuate        // start the full response (sprinkler on, route, dispatch)
	ActionRelease        // all-clear: the latch is released
)

func (a Action) String() string {
	return [...]string{"none", "warn", "actuate", "release"}[a]
}

// Config holds the escalation timings, in sim seconds. Values are starting
// points to tune (open item: heartbeat, TTL and persistence windows).
type Config struct {
	Mode            Mode
	FirePersist     float64 // fire tier must hold this long to escalate
	ArmedPersist    float64 // an armed room (smoke and CO, no heat) escalates after this long: the safety valve
	WarnPersist     float64 // a plain warning escalates after this long; 0 (the default) means never
	OperatorTimeout float64 // operator mode: fall back to persistence after this
	CancelHold      float64 // an operator cancel suppresses escalation this long
	ClearHold       float64 // all-clear: no signal for this long releases the latch
}

// DefaultConfig is the default policy: mode "both" (D4).
func DefaultConfig() Config {
	return Config{
		Mode: ModeBoth, FirePersist: 15, ArmedPersist: 120, WarnPersist: 0,
		OperatorTimeout: 60, CancelHold: 300, ClearHold: 300,
	}
}

// Decision is the escalator's output for one input.
type Decision struct {
	State  State
	Action Action
	Reason string
}

// AuditEntry records one event or transition for replay (NFR5, H3).
type AuditEntry struct {
	Time   float64
	Event  string
	Detail string
}

// Escalator turns a stream of tiers and operator actions into responses for
// one room. It is not safe for concurrent use; the detector owns one per room.
type Escalator struct {
	cfg   Config
	state State

	fireSince   float64 // when the fire tier started holding, or -1
	armedSince  float64 // when the armed tier started holding, or -1
	warnSince   float64 // when any non-none tier started holding, or -1
	pendingFrom float64 // operator mode: when persistence was first satisfied, or -1
	cancelUntil float64
	lastActive  float64 // last time any non-none tier was seen while latched

	audit []AuditEntry
}

// NewEscalator returns an escalator for one room, starting idle.
func NewEscalator(cfg Config) *Escalator {
	e := &Escalator{cfg: cfg}
	e.resetTimers()
	return e
}

// Audit returns the recorded events, oldest first.
func (e *Escalator) Audit() []AuditEntry { return append([]AuditEntry(nil), e.audit...) }

func (e *Escalator) resetTimers() {
	e.fireSince, e.armedSince, e.warnSince, e.pendingFrom = -1, -1, -1, -1
}

func (e *Escalator) record(t float64, event, format string, args ...any) {
	e.audit = append(e.audit, AuditEntry{Time: t, Event: event, Detail: fmt.Sprintf(format, args...)})
}

func (e *Escalator) decision(a Action, reason string) Decision {
	return Decision{State: e.state, Action: a, Reason: reason}
}

// Update feeds the tier observed at sim time now.
func (e *Escalator) Update(now float64, tier Tier) Decision {
	if e.state == StateFire {
		return e.updateLatched(now, tier)
	}

	// track how long each tier has been holding, in every non-latched state,
	// including while an operator cancel is in force
	switch tier {
	case TierNone:
		e.resetTimers()
	case TierWarning:
		e.fireSince, e.armedSince = -1, -1
		e.pendingFrom = -1
		if e.warnSince < 0 {
			e.warnSince = now
		}
	case TierArmed:
		e.fireSince = -1
		if e.armedSince < 0 {
			e.armedSince = now
		}
		if e.warnSince < 0 {
			e.warnSince = now
		}
	case TierFire:
		if e.fireSince < 0 {
			e.fireSince = now
		}
		if e.warnSince < 0 {
			e.warnSince = now
		}
	}

	if e.state == StateCancelled {
		if now < e.cancelUntil {
			return e.decision(ActionNone, "cancelled by operator")
		}
		e.state = StateIdle
		e.record(now, "cancel-expired", "cancel hold of %.0fs ended", e.cfg.CancelHold)
	}

	if tier == TierNone {
		e.state = StateIdle
		return e.decision(ActionNone, "")
	}

	first := e.state == StateIdle
	e.state = StateWarning

	switch {
	case tier == TierFire && now-e.fireSince >= e.cfg.FirePersist:
		return e.escalate(now, fmt.Sprintf("fire tier held %.0fs", now-e.fireSince))
	case tier == TierArmed && e.cfg.ArmedPersist > 0 && now-e.armedSince >= e.cfg.ArmedPersist:
		return e.escalate(now, fmt.Sprintf("armed (smoke and CO without heat) persisted %.0fs", now-e.armedSince))
	case e.cfg.WarnPersist > 0 && now-e.warnSince >= e.cfg.WarnPersist:
		return e.escalate(now, fmt.Sprintf("warning persisted %.0fs", now-e.warnSince))
	}
	if first {
		e.record(now, "warning", "tier %v", tier)
		return e.decision(ActionWarn, fmt.Sprintf("tier %v, pre-alert", tier))
	}
	return e.decision(ActionNone, "")
}

// escalate applies the mode: persistence and "both" actuate as soon as the
// persistence criterion is met; operator mode waits for a confirmation, and
// falls back to persistence after OperatorTimeout.
func (e *Escalator) escalate(now float64, why string) Decision {
	if e.cfg.Mode == ModeOperator {
		if e.pendingFrom < 0 {
			e.pendingFrom = now
			e.record(now, "awaiting-operator", "%s", why)
		}
		if now-e.pendingFrom < e.cfg.OperatorTimeout {
			return e.decision(ActionNone, "awaiting operator: "+why)
		}
		why += fmt.Sprintf(", operator timeout %.0fs, persistence fallback", e.cfg.OperatorTimeout)
	}
	return e.actuate(now, why)
}

func (e *Escalator) actuate(now float64, why string) Decision {
	e.state = StateFire
	e.lastActive = now
	e.resetTimers()
	e.record(now, "actuate", "%s", why)
	return e.decision(ActionActuate, why)
}

// updateLatched holds the response until the all-clear: no signal at all for
// ClearHold. A falling score alone never releases the sprinkler (FR7).
func (e *Escalator) updateLatched(now float64, tier Tier) Decision {
	if tier != TierNone {
		e.lastActive = now
		return e.decision(ActionNone, "")
	}
	if now-e.lastActive >= e.cfg.ClearHold {
		return e.release(now, "all-clear", fmt.Sprintf("no signal for %.0fs", now-e.lastActive))
	}
	return e.decision(ActionNone, "")
}

func (e *Escalator) release(now float64, event, why string) Decision {
	e.state = StateIdle
	e.resetTimers()
	e.record(now, event, "%s", why)
	return e.decision(ActionRelease, why)
}

// Confirm is the operator confirming a fire. It acts on a warning, or
// reverses an earlier cancel; with no warning there is nothing to confirm.
// Persistence mode ignores the operator.
func (e *Escalator) Confirm(now float64) Decision {
	if e.cfg.Mode == ModePersistence {
		e.record(now, "operator-ignored", "confirm: persistence mode")
		return e.decision(ActionNone, "operator not used in persistence mode")
	}
	switch e.state {
	case StateWarning, StateCancelled:
		e.record(now, "operator-confirm", "from %v", e.state)
		return e.actuate(now, "operator confirmed")
	}
	e.record(now, "operator-ignored", "confirm in state %v", e.state)
	return e.decision(ActionNone, "nothing to confirm")
}

// Cancel is the operator cancelling a warning. It suppresses escalation for
// CancelHold and then lapses, so a wrong cancel cannot hide a persisting fire
// (H3). A latched fire cannot be cancelled, only released.
func (e *Escalator) Cancel(now float64) Decision {
	if e.cfg.Mode == ModePersistence {
		e.record(now, "operator-ignored", "cancel: persistence mode")
		return e.decision(ActionNone, "operator not used in persistence mode")
	}
	if e.state != StateWarning {
		e.record(now, "operator-ignored", "cancel in state %v", e.state)
		return e.decision(ActionNone, "nothing to cancel")
	}
	e.state = StateCancelled
	e.cancelUntil = now + e.cfg.CancelHold
	e.record(now, "operator-cancel", "suppressed until t=%.0f", e.cancelUntil)
	return e.decision(ActionNone, "cancelled by operator")
}

// Release is the operator ending a latched response.
func (e *Escalator) Release(now float64) Decision {
	if e.state != StateFire {
		e.record(now, "operator-ignored", "release in state %v", e.state)
		return e.decision(ActionNone, "nothing to release")
	}
	return e.release(now, "operator-release", "released by operator")
}
