package physics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeSource map[string]string // actuator id -> state ("" = error)

func (f fakeSource) GetActuatorState(_ context.Context, id string) (string, error) {
	s, ok := f[id]
	if !ok || s == "" {
		return "", errors.New("unreachable")
	}
	return s, nil
}

func engineWithSprinklerIDs(scen ...Scenario) *Engine {
	return NewEngine(Config{
		Params: DefaultParams(), Yields: DefaultYields(), Sprinkler: DefaultSprinkler(),
		Rooms: []RoomSpec{
			{Name: "level0/A109", Volume: 150, SprinklerID: "A109-sprinkler"},
			{Name: "level0/A110", Volume: 150, SprinklerID: "A110-sprinkler"},
		},
		Scenarios: scen,
	})
}

func TestPollReadsSprinklerStateIntoEngine(t *testing.T) {
	e := engineWithSprinklerIDs(fire(0))
	src := fakeSource{"A109-sprinkler": "on", "A110-sprinkler": "off"}
	if errs := e.PollSprinklers(context.Background(), src); errs != 0 {
		t.Fatalf("errors = %d", errs)
	}
	e.mu.Lock()
	on, off := e.sprOn[0], e.sprOn[1]
	e.mu.Unlock()
	if on != 1 || off != 0 {
		t.Fatalf("sprOn = %v,%v want 1,0", on, off)
	}
}

func TestPollKeepsLastStateWhenBuildSimIsUnreachable(t *testing.T) {
	e := engineWithSprinklerIDs()
	e.SetSprinkler("level0/A109", 1)
	errs := e.PollSprinklers(context.Background(), fakeSource{}) // all fail
	if errs != 2 {
		t.Fatalf("errors = %d, want 2", errs)
	}
	e.mu.Lock()
	got := e.sprOn[0]
	e.mu.Unlock()
	if got != 1 {
		t.Fatalf("state was reset to %v on a read error", got)
	}
}

func TestParseSprinklerState(t *testing.T) {
	cases := map[string]float64{"on": 1, "ON": 1, "off": 0, "0.5": 0.5, "2": 1, "-1": 0, "junk": 0}
	for in, want := range cases {
		if got := parseSprinkler(in); got != want {
			t.Errorf("parseSprinkler(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRunTicksUntilCancelled(t *testing.T) {
	e := engineWithSprinklerIDs(fire(0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.Run(ctx, fakeSource{"A109-sprinkler": "off", "A110-sprinkler": "off"}, time.Millisecond, 1)
		close(done)
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if e.Time() < 5 {
		t.Fatalf("sim time %v, expected the loop to have ticked", e.Time())
	}
}

func TestTruthEndpointServesRoomState(t *testing.T) {
	e := engineWithSprinklerIDs(fire(0))
	for range 90 {
		e.Tick(1)
	}
	srv := httptest.NewServer(e.TruthHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/truth/rooms/level0/A109")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		Room    string  `json:"room"`
		TempC   float64 `json:"temp_c"`
		Smoke   float64 `json:"smoke"`
		COppm   float64 `json:"co_ppm"`
		SimTime float64 `json:"sim_time"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Room != "level0/A109" || got.TempC <= 20 || got.Smoke <= 0 || got.COppm <= 0 || got.SimTime != 90 {
		t.Fatalf("truth = %+v", got)
	}
}

func TestTruthEndpointReturns404ForUnknownRoom(t *testing.T) {
	srv := httptest.NewServer(engineWithSprinklerIDs().TruthHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/truth/rooms/level0/NOPE")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
