package outbox

import "strings"

// Policy says what happens to a message on topic that could not be sent:
// whether it is queued, the topic it is replayed on, and whether it may be
// shed when the outbox is full.
//
// The rule (D13): replay for history and for people, never for control.
//   - sensor readings are replayed on replay/sensors/..., which only the
//     db-writer reads: the detector and the actuator never see stale data.
//     They may be shed.
//   - decisions are replayed on replay/decisions/... for the audit trail only;
//     live consumers get the current state from the detector's periodic
//     refresh instead. Never shed.
//   - dispatches and dead letters are delivered late on their own topic:
//     responders told late beat responders never told. Never shed.
//   - commands, heartbeats and routes are never queued: a replayed sprinkler
//     command or heartbeat would act on, or report, a stale picture.
//
// Anything not named here is not queued.
func Policy(topic string) (replayTopic string, droppable, queue bool) {
	switch {
	case strings.HasPrefix(topic, "replay/"):
		return "", false, false
	case strings.HasPrefix(topic, "sensors/"):
		return "replay/" + topic, true, true
	case strings.HasPrefix(topic, "decisions/"):
		return "replay/" + topic, false, true
	case strings.HasPrefix(topic, "dispatch/"), strings.HasPrefix(topic, "dlq/"):
		return topic, false, true
	}
	return "", false, false
}
