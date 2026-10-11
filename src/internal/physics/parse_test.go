package physics

import "testing"

var roomIndex = map[string]int{"level0/A109": 0, "level0/A108": 1}

func TestParseBenignBuildsACookingScenario(t *testing.T) {
	sc, err := ParseBenign("cooking:level0/A108:30:600", roomIndex)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Kind != KindCooking || sc.Room != 1 || sc.T0 != 30 || sc.Duration != 600 {
		t.Fatalf("scenario = %+v", sc)
	}
}

func TestParseBenignBuildsADustScenario(t *testing.T) {
	sc, err := ParseBenign("dust:level0/A109:0:120", roomIndex)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Kind != KindDust || sc.Room != 0 || sc.Duration != 120 {
		t.Fatalf("scenario = %+v", sc)
	}
}

func TestParseBenignRejectsBadSpecs(t *testing.T) {
	for _, bad := range []string{
		"",                            // empty
		"cooking:level0/A109:0",       // missing duration
		"fire:level0/A109:0:60",       // not a benign kind
		"cooking:level9/NOPE:0:60",    // unknown room
		"cooking:level0/A109:x:60",    // start is not a number
		"cooking:level0/A109:0:-5",    // negative duration
		"cooking:level0/A109:0:0",     // zero duration
		"cooking:level0/A109:-1:60",   // negative start
		"cooking:level0/A109:0:60:99", // too many fields
	} {
		if _, err := ParseBenign(bad, roomIndex); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
