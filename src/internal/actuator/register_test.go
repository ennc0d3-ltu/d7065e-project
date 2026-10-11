package actuator

import "testing"

func TestSprinklerIDIsLevelQualified(t *testing.T) {
	if got := SprinklerID("level0/A109"); got != "level0-A109-sprinkler" {
		t.Fatalf("id = %q", got)
	}
	if SprinklerID("level0/A109") == SprinklerID("level1/A109") {
		t.Fatal("rooms with the same name on two floors share an actuator id")
	}
}

func TestEquipmentRegistersASprinklerWithAnOffActuator(t *testing.T) {
	eq := Equipment("level1/B204")
	if eq.Level != "level1" || eq.Room != "B204" || eq.Type != "sprinkler" || eq.Status != "running" {
		t.Fatalf("equipment = %+v", eq)
	}
	if len(eq.Actuators) != 1 || len(eq.Sensors) != 0 {
		t.Fatalf("want exactly one actuator and no sensors: %+v", eq)
	}
	a := eq.Actuators[0]
	if a.ID != "level1-B204-sprinkler" || a.State != "off" || a.Type != "sprinkler" {
		t.Fatalf("actuator = %+v", a)
	}
}

func TestEquipmentIDsDifferAcrossFloors(t *testing.T) {
	if Equipment("level0/A109").ID == Equipment("level1/A109").ID {
		t.Fatal("equipment ids collide across floors")
	}
}

func TestStateStringsMatchWhatThePhysicsReads(t *testing.T) {
	if StateString(true) != "on" || StateString(false) != "off" {
		t.Fatalf("on=%q off=%q", StateString(true), StateString(false))
	}
}
