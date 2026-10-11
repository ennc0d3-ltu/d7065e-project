package doors

import "testing"

func TestParseDoorsReadsRoomAndEntryNode(t *testing.T) {
	got, err := ParseDoors("level0/A1008:470, level0/A1024B:512")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Door{Room: "level0/A1008", EntryNode: 470}) || got[1].EntryNode != 512 {
		t.Fatalf("doors = %+v", got)
	}
}

func TestParseDoorsRejectsMistakes(t *testing.T) {
	for _, bad := range []string{
		"", "level0/A1008", "level0/A1008:", "level0/A1008:abc", "A1008:470", // not level/room
		"level0/A1008:470,level0/A1008:471", // the same room twice
		"level0/A1008:-3",
	} {
		if _, err := ParseDoors(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestBuildSimDoorsCarryTheLockStateAndAreClosedAndQualified(t *testing.T) {
	doors := []Door{{Room: "level0/A1008", EntryNode: 470}, {Room: "level1/A1008", EntryNode: 12}}
	got := BuildSimDoors(doors, map[string]Lock{"level0/A1008": Locked, "level1/A1008": Unlocked})
	if len(got) != 2 {
		t.Fatalf("got %d doors", len(got))
	}
	a, b := got[0], got[1]
	if a.LockState != "locked" || b.LockState != "unlocked" {
		t.Fatalf("lock states = %q, %q", a.LockState, b.LockState)
	}
	if a.ID == b.ID {
		t.Fatalf("both doors are called %q: room names repeat between floors", a.ID)
	}
	if a.Kind != "door" || a.State != "closed" || a.Level != "level0" || a.Room != "A1008" || a.EntryNodeID != 470 {
		t.Fatalf("door = %+v", a)
	}
}

func TestAMissingDesiredStateFailsSafeToUnlocked(t *testing.T) {
	got := BuildSimDoors([]Door{{Room: "level0/A1008", EntryNode: 470}}, map[string]Lock{})
	if got[0].LockState != "unlocked" {
		t.Fatalf("a door with no decision is %q, want unlocked", got[0].LockState)
	}
}
