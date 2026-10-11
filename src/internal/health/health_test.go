package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthzAlwaysOK(t *testing.T) {
	h := New("detector")
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", rec.Code)
	}
}

func TestReadyzFollowsReadiness(t *testing.T) {
	h := New("detector")
	get := func() int {
		rec := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	if got := get(); got != http.StatusServiceUnavailable {
		t.Fatalf("readyz before ready = %d, want 503", got)
	}
	h.SetReady(true)
	if got := get(); got != http.StatusOK {
		t.Fatalf("readyz after ready = %d, want 200", got)
	}
}

func TestHeartbeatAdvancesAndStales(t *testing.T) {
	now := time.Unix(1000, 0)
	h := New("detector")
	h.now = func() time.Time { return now }

	h.Beat()
	if h.Stale(2 * time.Second) {
		t.Fatal("fresh heartbeat reported stale")
	}
	now = now.Add(3 * time.Second)
	if !h.Stale(2 * time.Second) {
		t.Fatal("old heartbeat not reported stale")
	}
}
