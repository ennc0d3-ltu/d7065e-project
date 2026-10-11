package proto

import (
	"encoding/json"
	"testing"
)

func TestTopicsCarryTheRoomKey(t *testing.T) {
	if got := CommandTopic("level0/A109"); got != "commands/sprinkler/level0/A109" {
		t.Fatalf("command topic = %q", got)
	}
	if got := DecisionTopic("level0/A109"); got != "decisions/level0/A109" {
		t.Fatalf("decision topic = %q", got)
	}
	if got := HeartbeatTopic("detector"); got != "heartbeat/detector" {
		t.Fatalf("heartbeat topic = %q", got)
	}
}

func TestRoomFromTopicRecoversTheRoomKey(t *testing.T) {
	room, ok := RoomFromCommandTopic("commands/sprinkler/level1/B204")
	if !ok || room != "level1/B204" {
		t.Fatalf("room = %q ok=%v", room, ok)
	}
	if _, ok := RoomFromCommandTopic("decisions/level1/B204"); ok {
		t.Fatal("a decision topic was accepted as a command topic")
	}
	if _, ok := RoomFromCommandTopic("commands/sprinkler/"); ok {
		t.Fatal("an empty room was accepted")
	}
}

func TestCommandSurvivesAJSONRoundTrip(t *testing.T) {
	in := Command{Room: "level0/A109", Action: ActionOn, Reason: "fire tier held 15s",
		Seq: 4, TTL: 30, SimTime: 120.5}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Command
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip changed the command: %+v vs %+v", out, in)
	}
}

func TestCommandValidation(t *testing.T) {
	ok := Command{Room: "level0/A109", Action: ActionOn, Seq: 1, TTL: 30}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	bad := map[string]Command{
		"no room":      {Action: ActionOn, Seq: 1, TTL: 30},
		"bad action":   {Room: "r", Action: "explode", Seq: 1, TTL: 30},
		"no ttl":       {Room: "r", Action: ActionOn, Seq: 1},
		"negative ttl": {Room: "r", Action: ActionOn, Seq: 1, TTL: -5},
		"no sequence":  {Room: "r", Action: ActionOn, TTL: 30},
	}
	for name, c := range bad {
		if c.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, c)
		}
	}
}

func TestHeartbeatSurvivesAJSONRoundTripAndValidates(t *testing.T) {
	in := Heartbeat{Process: "detector", Seq: 3, SimTime: 55}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Heartbeat
	if err := json.Unmarshal(raw, &out); err != nil || out != in {
		t.Fatalf("round trip: %+v err=%v", out, err)
	}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	if (Heartbeat{Seq: 1}).Validate() == nil {
		t.Fatal("a heartbeat with no process name was accepted")
	}
}

func TestExpiryIsMeasuredInSimTime(t *testing.T) {
	c := Command{Room: "r", Action: ActionOn, Seq: 1, TTL: 30, SimTime: 100}
	if c.Expired(129.9) {
		t.Fatal("command expired before its TTL")
	}
	if !c.Expired(130) {
		t.Fatal("command did not expire at SimTime+TTL")
	}
}

func TestDoorCommandIsValidatedBeforeItReachesAnActuator(t *testing.T) {
	ok := DoorCommand{Room: "level0/A1008", Action: DoorUnlock, Seq: 1, TTL: 30, SimTime: 100, Reason: "fire"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a good command was rejected: %v", err)
	}
	for name, mutate := range map[string]func(*DoorCommand){
		"no room":     func(c *DoorCommand) { c.Room = "" },
		"bad action":  func(c *DoorCommand) { c.Action = "on" }, // a sprinkler word is not a door word
		"no sequence": func(c *DoorCommand) { c.Seq = 0 },
		"no TTL":      func(c *DoorCommand) { c.TTL = 0 },
	} {
		c := ok
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDoorCommandExpiresAfterItsTTL(t *testing.T) {
	c := DoorCommand{SimTime: 100, TTL: 30}
	if c.Expired(129) || !c.Expired(130) {
		t.Fatal("expiry must be at SimTime + TTL")
	}
}

func TestDoorTopicRoundTrips(t *testing.T) {
	topic := DoorTopic("level0/A1008")
	if topic != "commands/door/level0/A1008" {
		t.Fatalf("topic = %q", topic)
	}
	if room, ok := RoomFromDoorTopic(topic); !ok || room != "level0/A1008" {
		t.Fatalf("room = %q %v", room, ok)
	}
	if _, ok := RoomFromDoorTopic("commands/sprinkler/level0/A1008"); ok {
		t.Fatal("a sprinkler topic was read as a door topic")
	}
}
