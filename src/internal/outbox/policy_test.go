package outbox

import "testing"

func TestControlMessagesAreNeverQueuedForReplay(t *testing.T) {
	// replaying these after an outage would act on a stale picture
	for _, topic := range []string{
		"commands/sprinkler/level0/A109",
		"heartbeat/detector",
		"route/level0/A109", // a picture for the viewer; the next update repaints it
	} {
		if _, _, queue := Policy(topic); queue {
			t.Errorf("%s is queued for replay", topic)
		}
	}
}

func TestReadingsAreReplayedOnlyToTheHistoryTopicAndMayBeDropped(t *testing.T) {
	replay, droppable, queue := Policy("sensors/level0/A109/smoke")
	if !queue || !droppable || replay != "replay/sensors/level0/A109/smoke" {
		t.Fatalf("replay=%q droppable=%v queue=%v", replay, droppable, queue)
	}
}

func TestDecisionsAreReplayedToTheAuditTopicAndNeverDropped(t *testing.T) {
	replay, droppable, queue := Policy("decisions/level0/A109")
	if !queue || droppable || replay != "replay/decisions/level0/A109" {
		t.Fatalf("replay=%q droppable=%v queue=%v", replay, droppable, queue)
	}
}

func TestDispatchesAndDeadLettersGoToTheirOwnTopicAndAreNeverDropped(t *testing.T) {
	for _, topic := range []string{"dispatch/responders", "dispatch/ack", "dlq/bridge"} {
		replay, droppable, queue := Policy(topic)
		if !queue || droppable || replay != topic {
			t.Errorf("%s: replay=%q droppable=%v queue=%v", topic, replay, droppable, queue)
		}
	}
}

func TestUnknownTopicsAreNotQueued(t *testing.T) {
	if _, _, queue := Policy("something/else"); queue {
		t.Fatal("an unknown topic was queued: only what the policy names may be replayed")
	}
}

func TestReplayedTopicsAreNotRequeued(t *testing.T) {
	if _, _, queue := Policy("replay/sensors/level0/A109/co"); queue {
		t.Fatal("a replay message would be replayed again")
	}
}
