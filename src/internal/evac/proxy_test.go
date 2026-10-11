package evac

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type buildsim struct {
	srv  *httptest.Server
	mu   sync.Mutex
	puts map[string]string // path -> last body
	gets []string
}

func newBuildsim(t *testing.T) *buildsim {
	t.Helper()
	b := &buildsim{puts: map[string]string{}}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		if r.Method == http.MethodPut {
			b.puts[r.URL.Path] = string(body)
		} else {
			b.gets = append(b.gets, r.URL.Path)
		}
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"updated","version":1}`))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *buildsim) put(path string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.puts[path]
}

func send(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func entitiesJSON(t *testing.T, room string) string {
	b, _ := json.Marshal([]map[string]any{person("a", room)})
	return string(b)
}

func TestReadsPassStraightThroughToBuildSim(t *testing.T) {
	bs := newBuildsim(t)
	var n int
	p := NewProxy(bs.srv.URL, New(Config{Speed: 10}, router(&n)), func(context.Context) (map[string]bool, error) { return nil, nil }, func() float64 { return 0 })
	rec := send(p, http.MethodGet, "/api/building", "")
	if rec.Code != http.StatusOK || len(bs.gets) != 1 || bs.gets[0] != "/api/building" {
		t.Fatalf("code=%d gets=%v", rec.Code, bs.gets)
	}
}

func TestEntitiesOfPeopleInAFireRoomAreRewrittenOnTheirWayToBuildSim(t *testing.T) {
	bs := newBuildsim(t)
	var n int
	now := 0.0
	p := NewProxy(bs.srv.URL, New(Config{Speed: 10, Reaction: 0}, router(&n)),
		func(context.Context) (map[string]bool, error) { return map[string]bool{"level0/A1008": true}, nil },
		func() float64 { return now })
	send(p, http.MethodPut, "/api/entities", entitiesJSON(t, "A1008"))
	now = 5
	rec := send(p, http.MethodPut, "/api/entities", entitiesJSON(t, "A1008"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "updated") {
		t.Fatalf("BuildSim's answer was not returned: %d %s", rec.Code, rec.Body)
	}
	sent := bs.put("/api/entities")
	if strings.Contains(sent, `"room":"A1008"`) || !strings.Contains(sent, "evacuating") {
		t.Fatalf("BuildSim got %s", sent)
	}
}

func TestWhenTheFireSourceFailsPeopleAreLeftAlone(t *testing.T) {
	bs := newBuildsim(t)
	var n int
	p := NewProxy(bs.srv.URL, New(Config{Speed: 10}, router(&n)),
		func(context.Context) (map[string]bool, error) { return nil, errors.New("detector down") },
		func() float64 { return 0 })
	body := entitiesJSON(t, "A1008")
	send(p, http.MethodPut, "/api/entities", body)
	if got := bs.put("/api/entities"); got != body {
		t.Fatalf("with no fire information the body must pass unchanged, got %s", got)
	}
}

func TestOccupancyIsRewrittenAndRepublishedWhenEvacuationChanges(t *testing.T) {
	bs := newBuildsim(t)
	var n int
	now := 0.0
	p := NewProxy(bs.srv.URL, New(Config{Speed: 10, Reaction: 0}, router(&n)),
		func(context.Context) (map[string]bool, error) { return map[string]bool{"level0/A1008": true}, nil },
		func() float64 { return now })
	occ := `{"level0/A1008":{"persons":[{"id":"a","name":"N a","icon":"man"}],"aliens":[]}}`
	send(p, http.MethodPut, "/api/occupancy", occ)
	send(p, http.MethodPut, "/api/entities", entitiesJSON(t, "A1008"))
	now = 5
	send(p, http.MethodPut, "/api/entities", entitiesJSON(t, "A1008")) // a starts walking
	got := bs.put("/api/occupancy")
	if strings.Contains(got, "A1008") {
		t.Fatalf("the burning room is still counted: %s", got)
	}
}

func TestABadBodyIsPassedOnNotSwallowed(t *testing.T) {
	bs := newBuildsim(t)
	var n int
	p := NewProxy(bs.srv.URL, New(Config{Speed: 10}, router(&n)), func(context.Context) (map[string]bool, error) { return nil, nil }, func() float64 { return 0 })
	send(p, http.MethodPut, "/api/entities", "not json")
	if bs.put("/api/entities") != "not json" {
		t.Fatal("BuildSim should see (and reject) what the people simulator sent")
	}
}
