package gateway

import "testing"

func TestRoomSensorsGivesTemperatureSmokeAndCO(t *testing.T) {
	specs := RoomSensors("level0/A109", 1)
	if len(specs) != 3 {
		t.Fatalf("got %d specs, want 3", len(specs))
	}
	want := map[string]Kind{
		"level0-A109-temp":  KindTemperature,
		"level0-A109-smoke": KindSmoke,
		"level0-A109-co":    KindCO,
	}
	for _, s := range specs {
		if want[s.ID] != s.Kind || s.Room != "level0/A109" {
			t.Errorf("unexpected spec %+v", s)
		}
		if s.Sigma <= 0 || s.Lag <= 0 {
			t.Errorf("%s has no noise or lag: %+v", s.ID, s)
		}
	}
}

func TestRoomSensorsSeedsDiffer(t *testing.T) {
	specs := RoomSensors("level0/A109", 1)
	seen := map[int64]bool{}
	for _, s := range specs {
		if seen[s.Seed] {
			t.Fatalf("seed %d reused", s.Seed)
		}
		seen[s.Seed] = true
	}
}

func TestEquipmentRegistersOneRecordPerSensor(t *testing.T) {
	eq := Equipment(RoomSensors("level1/B204", 1))
	if len(eq) != 3 {
		t.Fatalf("got %d equipment, want 3", len(eq))
	}
	types := map[string]string{}
	for _, e := range eq {
		if e.Level != "level1" || e.Room != "B204" || e.Status != "running" {
			t.Errorf("bad placement: %+v", e)
		}
		if len(e.Sensors) != 1 || len(e.Actuators) != 0 {
			t.Errorf("want exactly one sensor and no actuators: %+v", e)
		}
		if e.Sensors[0].DataType != "text" || e.Sensors[0].Unit == "" {
			t.Errorf("sensor must be text with a unit: %+v", e.Sensors[0])
		}
		types[e.Sensors[0].ID] = e.Type
	}
	want := map[string]string{
		"level1-B204-temp": "temperature_sensor", "level1-B204-smoke": "smoke_detector", "level1-B204-co": "co_sensor",
	}
	for id, typ := range want {
		if types[id] != typ {
			t.Errorf("%s registered as %q, want %q", id, types[id], typ)
		}
	}
}

func TestEquipmentIDsAreUniqueAcrossRooms(t *testing.T) {
	all := append(Equipment(RoomSensors("level0/A109", 1)), Equipment(RoomSensors("level1/A109", 2))...)
	seen := map[string]bool{}
	for _, e := range all {
		if seen[e.ID] {
			t.Fatalf("duplicate equipment id %q (room names repeat between floors)", e.ID)
		}
		seen[e.ID] = true
		for _, s := range e.Sensors {
			if seen["sensor:"+s.ID] {
				t.Fatalf("duplicate sensor id %q: BuildSim needs globally unique ids", s.ID)
			}
			seen["sensor:"+s.ID] = true
		}
	}
}

func TestOnlyTheChosenKindsAreKept(t *testing.T) {
	var all []SensorSpec
	for i, r := range []string{"level0/A1", "level0/A2"} {
		all = append(all, RoomSensors(r, int64(i))...)
	}
	got, err := FilterKinds(all, []string{"smoke"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("kept %d, want one smoke sensor per room", len(got))
	}
	for _, s := range got {
		if s.Kind != KindSmoke {
			t.Fatalf("kept a %s sensor", s.Kind)
		}
	}
}

func TestSeveralKindsAndTheDefaultOfAll(t *testing.T) {
	all := RoomSensors("level0/A1", 1)
	two, _ := FilterKinds(all, []string{"co", "temperature"})
	if len(two) != 2 {
		t.Fatalf("two kinds kept %d", len(two))
	}
	every, err := FilterKinds(all, nil)
	if err != nil || len(every) != 3 {
		t.Fatalf("no filter kept %d (%v), want all 3", len(every), err)
	}
}

func TestFilteringKeepsEachSensorsNoiseStreamAndId(t *testing.T) {
	// a sensor must behave the same whether it runs alone or with the others
	all := RoomSensors("level0/A1", 7)
	alone, _ := FilterKinds(all, []string{"co"})
	for _, a := range all {
		if a.Kind == KindCO && (alone[0].ID != a.ID || alone[0].Seed != a.Seed) {
			t.Fatalf("filtering changed the sensor: %+v vs %+v", alone[0], a)
		}
	}
}

func TestAnUnknownKindIsAnErrorNotSilentlyNothing(t *testing.T) {
	if _, err := FilterKinds(RoomSensors("level0/A1", 1), []string{"smoke", "humidity"}); err == nil {
		t.Fatal("a misspelt kind produced a gateway with no sensors")
	}
}
