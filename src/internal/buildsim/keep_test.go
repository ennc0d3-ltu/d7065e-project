package buildsim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestKeepRegisteredRegistersAgainAfterBuildSimForgetsEverything(t *testing.T) {
	var mu sync.Mutex
	known := map[string]bool{}
	created := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/equipment/bulk" {
			http.NotFound(w, r)
			return
		}
		var eq []Equipment
		_ = json.NewDecoder(r.Body).Decode(&eq)
		mu.Lock()
		defer mu.Unlock()
		res := BulkResult{Total: len(eq)}
		for _, e := range eq {
			if known[e.ID] {
				res.Skipped++
			} else {
				known[e.ID] = true
				res.Created++
				created++
			}
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer srv.Close()

	c := New(srv.URL, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		KeepRegistered(ctx, c, []Equipment{{ID: "a"}, {ID: "b"}}, 10*time.Millisecond, nil)
		close(done)
	}()

	waitCreated := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := created
			mu.Unlock()
			if got >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("created never reached %d", n)
	}
	waitCreated(2)
	mu.Lock()
	known = map[string]bool{} // BuildSim restarted: all state gone
	mu.Unlock()
	waitCreated(4)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("KeepRegistered did not stop when cancelled")
	}
}

func TestKeepRegisteredSurvivesAnUnreachableBuildSim(t *testing.T) {
	c := New("http://127.0.0.1:1", 100*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	errs := 0
	KeepRegistered(ctx, c, []Equipment{{ID: "a"}}, 20*time.Millisecond, func(_ BulkResult, err error) {
		if err != nil {
			errs++
		}
	})
	if errs == 0 {
		t.Fatal("failures were not reported to the callback")
	}
}
