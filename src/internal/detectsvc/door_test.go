package detectsvc

import (
	"encoding/json"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

func (c *capture) doorCommands(t *testing.T, room string) []proto.DoorCommand {
	t.Helper()
	var out []proto.DoorCommand
	for _, m := range c.msgs {
		if m.topic == proto.DoorTopic(room) {
			var cmd proto.DoorCommand
			if err := json.Unmarshal(m.payload, &cmd); err != nil {
				t.Fatal(err)
			}
			out = append(out, cmd)
		}
	}
	return out
}

func TestAQuietRoomsDoorIsNeverUnlocked(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 60; now += 5 {
		calm(t, s, roomA, now)
		tick(t, s, now)
	}
	if got := pub.doorCommands(t, roomA); len(got) != 0 {
		t.Fatalf("a quiet room got door commands: %+v", got)
	}
}

func TestDustAloneDoesNotUnlockTheDoor(t *testing.T) {
	// a warning is a pre-alert, not an alarm: the door stays locked
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 120; now += 5 {
		feed(t, s, roomA, now, 0.2, 3, 21)
		tick(t, s, now)
	}
	if got := pub.doorCommands(t, roomA); len(got) != 0 {
		t.Fatalf("smoke alone unlocked a door: %+v", got)
	}
}

func TestAnAlarmUnlocksTheDoorAtOnceWithoutWaitingForTheSprinkler(t *testing.T) {
	// smoke and CO without heat arm the room (the pre-action gate keeps the
	// sprinkler off) but it is an alarm, and an alarm releases the door
	pub := &capture{}
	s := newModelSvc(t, pub)
	for now := 0.0; now <= 60; now += 5 {
		drift(t, s, roomA, now)
		tick(t, s, now)
	}
	doors := pub.doorCommands(t, roomA)
	if len(doors) == 0 {
		t.Fatal("an armed room did not release its door")
	}
	if got := pub.commands(t, roomA); len(got) != 0 {
		t.Fatalf("the sprinkler was commanded on an armed room: %+v", got)
	}
	if err := doors[0].Validate(); err != nil {
		t.Fatalf("invalid door command: %v", err)
	}
}

func TestAFireKeepsTheDoorUnlockedWithRisingSequenceAndATTL(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 60; now += 5 {
		fire(t, s, roomA, now)
		tick(t, s, now)
	}
	doors := pub.doorCommands(t, roomA)
	if len(doors) < 5 {
		t.Fatalf("only %d door commands in a minute of fire: it must be re-sent every tick", len(doors))
	}
	for i := 1; i < len(doors); i++ {
		if doors[i].Seq <= doors[i-1].Seq {
			t.Fatalf("sequence did not rise: %+v then %+v", doors[i-1], doors[i])
		}
	}
	if doors[0].TTL != DefaultConfig().CommandTTL || doors[0].Action != proto.DoorUnlock || doors[0].Reason == "" {
		t.Fatalf("first door command = %+v", doors[0])
	}
}

func TestOnlyTheRoomInAlarmGetsADoorCommand(t *testing.T) {
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 40; now += 5 {
		fire(t, s, roomA, now)
		calm(t, s, roomB, now)
		tick(t, s, now)
	}
	if len(pub.doorCommands(t, roomA)) == 0 {
		t.Fatal("the burning room's door was not released")
	}
	if got := pub.doorCommands(t, roomB); len(got) != 0 {
		t.Fatalf("a calm neighbour's door was released: %+v", got)
	}
}

func TestADoorCommandIsStillSentWhileTheFireIsLatchedAndTheScoreFalls(t *testing.T) {
	// the door must not lock behind the people while the response is latched
	pub := &capture{}
	s := newSvc(pub)
	for now := 0.0; now <= 30; now += 5 {
		fire(t, s, roomA, now)
		tick(t, s, now)
	}
	n := len(pub.doorCommands(t, roomA))
	for now := 35.0; now <= 100; now += 5 { // readings fall back to normal
		calm(t, s, roomA, now)
		tick(t, s, now)
	}
	if got := len(pub.doorCommands(t, roomA)); got <= n+5 {
		t.Fatalf("door commands stopped (%d, was %d) while the sprinkler is latched on", got, n)
	}
}

func TestADoorCommandThatCouldNotBePublishedIsSentAgainOnTheNextTick(t *testing.T) {
	pub := &capture{failing: 2}
	s := newSvc(pub)
	for now := 0.0; now <= 40; now += 5 {
		fire(t, s, roomA, now)
		tick(t, s, now)
	}
	if len(pub.doorCommands(t, roomA)) == 0 {
		t.Fatal("a failed publish was never repeated")
	}
}
