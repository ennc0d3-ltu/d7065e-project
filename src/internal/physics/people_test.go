package physics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPollPeopleSetsTheHeadCountOfKnownRooms(t *testing.T) {
	e := engineWithSprinklerIDs()
	errs := e.PollPeople(context.Background(), func(context.Context) (map[string]int, error) {
		return map[string]int{"level0/A109": 4, "level9/elsewhere": 7}, nil
	})
	if errs != 0 {
		t.Fatalf("errors = %d", errs)
	}
	e.mu.Lock()
	got := e.people[0]
	e.mu.Unlock()
	if got != 4 {
		t.Fatalf("people in A109 = %d, want 4", got)
	}
}

func TestPollPeopleEmptiesARoomTheSourceNoLongerLists(t *testing.T) {
	e := engineWithSprinklerIDs()
	src := map[string]int{"level0/A109": 4}
	poll := func(context.Context) (map[string]int, error) { return src, nil }
	e.PollPeople(context.Background(), poll)
	src = map[string]int{} // everyone left
	e.PollPeople(context.Background(), poll)
	e.mu.Lock()
	got := e.people[0]
	e.mu.Unlock()
	if got != 0 {
		t.Fatalf("people = %d after the room emptied", got)
	}
}

func TestPollPeopleKeepsTheLastCountWhenTheSourceFails(t *testing.T) {
	e := engineWithSprinklerIDs()
	e.SetPeople("level0/A109", 3)
	if errs := e.PollPeople(context.Background(), func(context.Context) (map[string]int, error) {
		return nil, errors.New("buildsim down")
	}); errs != 1 {
		t.Fatalf("errors = %d", errs)
	}
	e.mu.Lock()
	got := e.people[0]
	e.mu.Unlock()
	if got != 3 {
		t.Fatalf("people = %d: an unreachable source must not empty the building", got)
	}
}

func TestAlignOccupancyClockSetsSpeedStartAndRunning(t *testing.T) {
	var body map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if err := AlignOccupancyClock(context.Background(), srv.URL, 50, "10:30"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/clock" || body["factor"] != 50.0 || body["time"] != "10:30" || body["running"] != true {
		t.Fatalf("path=%s body=%v", path, body)
	}
}

func TestAlignOccupancyClockReportsARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"bad factor"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	if err := AlignOccupancyClock(context.Background(), srv.URL, -1, "10:30"); err == nil {
		t.Fatal("a refused clock change was reported as success")
	}
}
