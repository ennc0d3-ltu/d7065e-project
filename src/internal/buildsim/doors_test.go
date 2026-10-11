package buildsim

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestPutDoorsSendsTheWholeCollection(t *testing.T) {
	var method, path string
	var body []map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"status":"updated"}`))
	})
	doors := []Door{
		{ID: "door-level0-A1008", Name: "Door A1008", Kind: "door", Level: "level0", Room: "A1008", EntryNodeID: 470, State: "closed", LockState: "locked"},
		{ID: "door-level0-A1024B", Name: "Door A1024B", Kind: "door", Level: "level0", Room: "A1024B", EntryNodeID: 512, State: "closed", LockState: "unlocked"},
	}
	if err := c.PutDoors(context.Background(), doors); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/api/doors" || len(body) != 2 {
		t.Fatalf("%s %s with %d doors", method, path, len(body))
	}
	if body[0]["entry_node_id"] != 470.0 || body[0]["lock_state"] != "locked" || body[0]["state"] != "closed" || body[0]["room"] != "A1008" {
		t.Fatalf("first door = %v", body[0])
	}
}

func TestPutDoorsWithNoDoorsSendsAnEmptyArrayNotNull(t *testing.T) {
	var raw []byte
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.PutDoors(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Fatalf("body = %s", raw)
	}
}

func TestGetDoorsReadsTheDoorsAndTreatsNullAsNone(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"d1","room":"A1008","level":"level0","entry_node_id":470,"state":"closed","lock_state":"locked","blocked":true}]`))
	})
	got, err := c.GetDoors(context.Background())
	if err != nil || len(got) != 1 || got[0].LockState != "locked" || got[0].EntryNodeID != 470 {
		t.Fatalf("doors = %+v, err %v", got, err)
	}
	none := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`null`)) })
	if got, err := none.GetDoors(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("null gave %+v, err %v", got, err)
	}
}
