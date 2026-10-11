package physics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIgniteStartsAFireInThePickedRoomAtTheCurrentSimTime(t *testing.T) {
	e := testEngine() // three rooms, no scenarios
	advance(e, 50)
	if err := e.Ignite("level0/A110", "fast"); err != nil {
		t.Fatal(err)
	}
	advance(e, 120)
	hot, _ := e.Truth("level0/A110")
	other, _ := e.Truth("level0/A109")
	if hot.T-20 < 10 || hot.K < 0.1 {
		t.Fatalf("the picked room did not burn: %+v", hot)
	}
	if other.K != 0 { // A109 and A110 share a door in testEngine
		// smoke may spread through the door, but the heat source is only in A110
		if other.T-20 > hot.T-20 {
			t.Fatalf("the neighbour is hotter than the room on fire: %+v vs %+v", other, hot)
		}
	}
	active := e.Active()
	if len(active) != 1 || active[0].Room != "level0/A110" || active[0].StartedAt != 50 || active[0].Kind != "fire" {
		t.Fatalf("active = %+v, want one fire in A110 started at sim 50", active)
	}
}

func TestIgniteRejectsAnUnknownRoomOrGrowth(t *testing.T) {
	e := testEngine()
	if err := e.Ignite("level9/nope", "fast"); err == nil {
		t.Error("an unknown room was accepted")
	}
	if err := e.Ignite("level0/A109", "explosive"); err == nil {
		t.Error("an unknown growth class was accepted")
	}
	if len(e.Active()) != 0 {
		t.Fatal("a rejected request left a scenario behind")
	}
}

func TestBenignSourceRunsForItsDurationThenStops(t *testing.T) {
	e := testEngine()
	if err := e.StartBenign("cooking", "level0/A109", 60); err != nil {
		t.Fatal(err)
	}
	if got := e.Active(); len(got) != 1 || got[0].Kind != "cooking" {
		t.Fatalf("active = %+v", got)
	}
	advance(e, 30)
	mid, _ := e.Truth("level0/A109")
	if mid.K <= 0 {
		t.Fatal("cooking produced no smoke while it ran")
	}
}

func TestBenignRejectsBadInput(t *testing.T) {
	e := testEngine()
	for _, c := range []struct {
		kind, room string
		dur        float64
	}{{"arson", "level0/A109", 60}, {"cooking", "level0/none", 60}, {"cooking", "level0/A109", 0}, {"dust", "level0/A109", -5}} {
		if err := e.StartBenign(c.kind, c.room, c.dur); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestClearStopsEverySourceButNotTheSmokeAlreadyThere(t *testing.T) {
	e := testEngine()
	_ = e.Ignite("level0/B200", "fast")
	advance(e, 90)
	before, _ := e.Truth("level0/B200")
	e.Clear()
	if len(e.Active()) != 0 {
		t.Fatal("scenarios survived Clear")
	}
	advance(e, 400)
	after, _ := e.Truth("level0/B200")
	if after.T >= before.T {
		t.Fatalf("the room kept heating after the fire was cleared: %v -> %v", before.T, after.T)
	}
	if after.K <= 0 {
		t.Fatal("clearing the source removed the smoke instantly: it must decay, not vanish")
	}
}

func TestSeveralRoomsCanBeOnFireAtOnce(t *testing.T) {
	e := testEngine()
	_ = e.Ignite("level0/A109", "medium")
	_ = e.Ignite("level0/B200", "slow")
	if len(e.Active()) != 2 {
		t.Fatalf("active = %+v", e.Active())
	}
}

func TestRoomNamesAreListedForThePicker(t *testing.T) {
	got := testEngine().RoomNames()
	if len(got) != 3 || got[0] != "level0/A109" {
		t.Fatalf("rooms = %v", got)
	}
}

// --- HTTP ---

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func TestControlFireEndpointIgnitesTheRoom(t *testing.T) {
	e := testEngine()
	h := e.ControlHandler()
	rec := post(h, "/control/fire", `{"room":"level0/A109","growth":"medium"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body)
	}
	if len(e.Active()) != 1 {
		t.Fatalf("active = %+v", e.Active())
	}
}

func TestControlEndpointsRejectBadRequests(t *testing.T) {
	h := testEngine().ControlHandler()
	for _, c := range []struct {
		path, body string
		want       int
	}{
		{"/control/fire", `{"room":"level9/none","growth":"fast"}`, http.StatusNotFound},
		{"/control/fire", `{"room":"level0/A109","growth":"huge"}`, http.StatusBadRequest},
		{"/control/fire", `nope`, http.StatusBadRequest},
		{"/control/benign", `{"kind":"arson","room":"level0/A109","duration":60}`, http.StatusBadRequest},
		{"/control/benign", `{"kind":"cooking","room":"level9/none","duration":60}`, http.StatusNotFound},
	} {
		if rec := post(h, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.path, c.body, rec.Code, c.want)
		}
	}
}

func TestControlStateListsRoomsAndWhatIsActive(t *testing.T) {
	e := testEngine()
	h := e.ControlHandler()
	post(h, "/control/benign", `{"kind":"dust","room":"level0/A109","duration":300}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/control/state", nil))
	var st struct {
		SimTime float64
		Rooms   []string
		Active  []struct{ Kind, Room string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if len(st.Rooms) != 3 || len(st.Active) != 1 || st.Active[0].Kind != "dust" {
		t.Fatalf("state = %+v", st)
	}
}

func TestControlClearEndpoint(t *testing.T) {
	e := testEngine()
	h := e.ControlHandler()
	post(h, "/control/fire", `{"room":"level0/A109","growth":"fast"}`)
	if rec := post(h, "/control/clear", ``); rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rec.Code)
	}
	if len(e.Active()) != 0 {
		t.Fatal("clear did nothing")
	}
}

func TestResetReturnsEveryRoomToAmbientAndStopsEverySource(t *testing.T) {
	e := testEngine()
	_ = e.Ignite("level0/B200", "fast")
	advance(e, 200)
	if hot, _ := e.Truth("level0/B200"); hot.T-20 < 5 {
		t.Fatal("setup: the room did not heat up")
	}
	e.Reset()
	if len(e.Active()) != 0 {
		t.Fatal("scenarios survived Reset")
	}
	for _, name := range e.RoomNames() {
		r, _ := e.Truth(name)
		if r.K != 0 || r.C != 0 || r.T != 20 {
			t.Fatalf("%s not at ambient after Reset: %+v", name, r)
		}
	}
}

func TestResetDoesNotRewindTheClock(t *testing.T) {
	e := testEngine()
	advance(e, 100)
	before := e.Time()
	e.Reset()
	if e.Time() != before {
		t.Fatalf("sim time moved from %v to %v: every consumer's clock would jump", before, e.Time())
	}
}

func TestControlResetEndpoint(t *testing.T) {
	e := testEngine()
	h := e.ControlHandler()
	post(h, "/control/fire", `{"room":"level0/A109","growth":"fast"}`)
	if rec := post(h, "/control/reset", ``); rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rec.Code)
	}
	if len(e.Active()) != 0 {
		t.Fatal("reset left a source behind")
	}
}
