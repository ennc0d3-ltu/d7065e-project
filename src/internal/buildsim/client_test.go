package buildsim

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, time.Second)
}

func TestPutSensorValueSendsTextValue(t *testing.T) {
	var gotPath, gotMethod string
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	})
	if err := c.PutSensorValue(context.Background(), "A109-smoke", "0.042"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/sensors/A109-smoke/value" {
		t.Fatalf("got %s %s", gotMethod, gotPath)
	}
	if body["data_type"] != "text" || body["value"] != "0.042" {
		t.Fatalf("body = %v", body)
	}
}

func TestGetActuatorStateReadsState(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/actuators/A109-sprinkler" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":"A109-sprinkler","type":"sprinkler","state":"on"}`))
	})
	got, err := c.GetActuatorState(context.Background(), "A109-sprinkler")
	if err != nil {
		t.Fatal(err)
	}
	if got != "on" {
		t.Fatalf("state = %q, want on", got)
	}
}

func TestPutActuatorStateSendsState(t *testing.T) {
	var gotPath string
	var body map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
	})
	if err := c.PutActuatorState(context.Background(), "A109-sprinkler", "on"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/actuators/A109-sprinkler/state" || body["state"] != "on" {
		t.Fatalf("path=%s body=%v", gotPath, body)
	}
}

func TestNotFoundIsReportedAsErrNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sensor not found", http.StatusNotFound)
	})
	err := c.PutSensorValue(context.Background(), "nope", "1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestServerErrorCarriesStatusAndBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	err := c.PutActuatorState(context.Background(), "x", "on")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusInternalServerError {
		t.Fatalf("err = %v, want *StatusError 500", err)
	}
}

func TestSlowServerTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	c := New(srv.URL, 50*time.Millisecond)
	start := time.Now()
	err := c.PutSensorValue(context.Background(), "x", "1")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("timeout not honoured, took %v", time.Since(start))
	}
}

func TestGetOccupancyParsesPeoplePerRoom(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/occupancy" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"level0/A109":{"persons":[{"id":"p1","name":"Alice","icon":"woman"},{"id":"p2","name":"Bob","icon":"man"}],"aliens":[]},"level0/A110":{"persons":[],"aliens":[]}}`))
	})
	occ, err := c.GetOccupancy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(occ["level0/A109"].Persons) != 2 || occ["level0/A109"].Persons[0].Name != "Alice" || len(occ["level0/A110"].Persons) != 0 {
		t.Fatalf("occupancy = %+v", occ)
	}
}

func TestPutRoomLayersSendsTheWholeCollection(t *testing.T) {
	var gotPath, gotMethod string
	var body []map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
	})
	err := c.PutRoomLayers(context.Background(), []Layer{{
		ID: "fire-risk", Label: "Fire risk", Unit: "tier", Source: "decision-service estimate",
		Minimum: 0, Maximum: 2, Opacity: 0.7, Palette: []string{"#22c55e", "#facc15", "#dc2626"},
		Values: map[string]float64{"level0/A109": 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/room-layers" || len(body) != 1 || body[0]["id"] != "fire-risk" {
		t.Fatalf("%s %s body=%v", gotMethod, gotPath, body)
	}
}

func TestEmptyCollectionsAreSentAsAnEmptyArrayNotNull(t *testing.T) {
	// BuildSim replaces the whole collection; null would not clear it
	for name, put := range map[string]func(*Client) error{
		"effects": func(c *Client) error { return c.PutEffects(context.Background(), nil) },
		"alerts":  func(c *Client) error { return c.PutAlerts(context.Background(), nil) },
		"layers":  func(c *Client) error { return c.PutRoomLayers(context.Background(), nil) },
	} {
		var raw []byte
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			raw, _ = io.ReadAll(r.Body)
		})
		if err := put(c); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(raw) != "[]" {
			t.Errorf("%s: body = %q, want []", name, raw)
		}
	}
}

func TestPutEffectsAndAlertsUseTheirEndpoints(t *testing.T) {
	var paths []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
	})
	_ = c.PutEffects(context.Background(), []Effect{{ID: "fire-A109", Type: "fire", Level: "level0", Room: "A109", Radius: 7, Height: 15, Intensity: 0.9}})
	_ = c.PutAlerts(context.Background(), []Alert{{ID: "a", Severity: "critical", Title: "Fire", Message: "m", Level: "level0", Room: "A109"}})
	if len(paths) != 2 || paths[0] != "PUT /api/effects" || paths[1] != "PUT /api/alerts" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestRouteAsksForAWalkableCrossFloorRoute(t *testing.T) {
	var q map[string][]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/graph/route" {
			http.NotFound(w, r)
			return
		}
		q = r.URL.Query()
		_, _ = w.Write([]byte(`{"path":[{"room_id":1,"name":"A109","level":"level0","x":1,"y":2},{"room_id":2,"name":"CORR","level":"level0","x":3,"y":4},{"room_id":3,"name":"EXIT","level":"level0","x":5,"y":6}],"distance":12.5}`))
	})
	route, err := c.Route(context.Background(), "level0", "A109", "level0", "EXIT")
	if err != nil {
		t.Fatal(err)
	}
	if len(route.Path) != 3 || route.Distance != 12.5 || route.Path[2].Name != "EXIT" {
		t.Fatalf("route = %+v", route)
	}
	for k, want := range map[string]string{"from_name": "A109", "from_level": "level0", "to_name": "EXIT", "to_level": "level0", "type": "walkable"} {
		if len(q[k]) != 1 || q[k][0] != want {
			t.Errorf("query %s = %v, want %s", k, q[k], want)
		}
	}
}

func TestRouteWithNoPathIsErrNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	if _, err := c.Route(context.Background(), "level0", "A", "level0", "B"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestBulkCreateReportsCounts(t *testing.T) {
	var got []Equipment
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/equipment/bulk" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"created":1,"skipped":1,"total":2,"version":5}`))
	})
	eq := []Equipment{
		{ID: "smoke-A109", Name: "Smoke A109", Type: "smoke_detector", Category: "monitoring",
			Level: "level0", Room: "A109", Status: "running",
			Sensors: []Sensor{{ID: "A109-smoke", Name: "Smoke", Type: "smoke", DataType: "text", Unit: "1/m"}}},
		{ID: "spr-A109", Name: "Sprinkler A109", Type: "sprinkler", Category: "fire",
			Level: "level0", Room: "A109", Status: "running",
			Actuators: []Actuator{{ID: "A109-sprinkler", Name: "Sprinkler", Type: "sprinkler", State: "off"}}},
	}
	res, err := c.BulkCreate(context.Background(), eq)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.Skipped != 1 || len(got) != 2 || got[0].Sensors[0].ID != "A109-smoke" {
		t.Fatalf("res=%+v got=%+v", res, got)
	}
}
