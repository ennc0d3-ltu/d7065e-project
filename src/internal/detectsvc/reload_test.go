package detectsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/registry"
)

type applied struct {
	mu    sync.Mutex
	th    []detector.Thresholds
	model []bool
}

func (a *applied) Apply(th detector.Thresholds, m *detector.Model) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.th = append(a.th, th)
	a.model = append(a.model, m != nil)
}

func (a *applied) count() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.th) }

// fakeRegistry serves whatever bundle (or status) the test sets.
type fakeRegistry struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bundle *registry.Bundle
	status int
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	f := &fakeRegistry{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.bundle == nil {
			http.Error(w, "none", http.StatusNotFound)
			return
		}
		if f.status != http.StatusOK {
			http.Error(w, "boom", f.status)
			return
		}
		_ = json.NewEncoder(w).Encode(f.bundle)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) set(b *registry.Bundle, status int) {
	f.mu.Lock()
	f.bundle, f.status = b, status
	f.mu.Unlock()
}

func bundleV(v int, smokeWarn float64) *registry.Bundle {
	th := detector.DefaultThresholds()
	th.SmokeWarn = smokeWarn
	return &registry.Bundle{Version: v, Status: registry.Active, Thresholds: th}
}

func newLoader(t *testing.T, f *fakeRegistry, a Applier, cache string) *Loader {
	return &Loader{URL: f.srv.URL, CachePath: cache, Applier: a, Client: &http.Client{Timeout: time.Second}}
}

func TestANewActiveBundleIsAppliedOnce(t *testing.T) {
	f, a := newFakeRegistry(t), &applied{}
	f.set(bundleV(1, 0.04), http.StatusOK)
	l := newLoader(t, f, a, filepath.Join(t.TempDir(), "c.json"))
	for i := 0; i < 3; i++ {
		l.Poll(context.Background())
	}
	if a.count() != 1 || a.th[0].SmokeWarn != 0.04 || l.Version() != 1 {
		t.Fatalf("applied %d times, version %d", a.count(), l.Version())
	}
	f.set(bundleV(2, 0.03), http.StatusOK)
	l.Poll(context.Background())
	if a.count() != 2 || l.Version() != 2 {
		t.Fatalf("new version not applied: %d, v%d", a.count(), l.Version())
	}
}

func TestABundleBreakingTheHardLimitsIsRefusedAndTheLastGoodStays(t *testing.T) {
	f, a := newFakeRegistry(t), &applied{}
	f.set(bundleV(1, 0.04), http.StatusOK)
	l := newLoader(t, f, a, filepath.Join(t.TempDir(), "c.json"))
	l.Poll(context.Background())
	bad := bundleV(2, 0.04)
	bad.Thresholds.COFire = detector.MaxCOFire + 500
	f.set(bad, http.StatusOK)
	l.Poll(context.Background())
	if a.count() != 1 || l.Version() != 1 {
		t.Fatalf("a bad bundle was applied: %d applies, version %d", a.count(), l.Version())
	}
	if l.LastError() == "" {
		t.Fatal("the refusal was not recorded")
	}
}

func TestRegistryDownOrSlowKeepsTheLastGoodBundle(t *testing.T) {
	f, a := newFakeRegistry(t), &applied{}
	f.set(bundleV(1, 0.04), http.StatusOK)
	l := newLoader(t, f, a, filepath.Join(t.TempDir(), "c.json"))
	l.Poll(context.Background())
	f.set(bundleV(2, 0.03), http.StatusInternalServerError)
	l.Poll(context.Background())
	f.srv.Close()
	l.Poll(context.Background())
	if a.count() != 1 || l.Version() != 1 {
		t.Fatalf("lost the last good bundle: %d applies, v%d", a.count(), l.Version())
	}
}

func TestNoActiveBundleChangesNothing(t *testing.T) {
	f, a := newFakeRegistry(t), &applied{}
	l := newLoader(t, f, a, filepath.Join(t.TempDir(), "c.json"))
	l.Poll(context.Background())
	if a.count() != 0 || l.Version() != 0 {
		t.Fatal("applied something with no active bundle")
	}
}

func TestACachedBundleStartsTheDetectorWithTheRegistryDown(t *testing.T) {
	f, a := newFakeRegistry(t), &applied{}
	cache := filepath.Join(t.TempDir(), "c.json")
	f.set(bundleV(3, 0.05), http.StatusOK)
	newLoader(t, f, a, cache).Poll(context.Background())
	f.srv.Close() // the registry is gone when the detector restarts

	b := &applied{}
	l := newLoader(t, f, b, cache)
	if !l.LoadCache() {
		t.Fatal("cache not loaded")
	}
	l.Poll(context.Background()) // registry unreachable: nothing changes
	if b.count() != 1 || b.th[0].SmokeWarn != 0.05 || l.Version() != 3 {
		t.Fatalf("cache start: %d applies, v%d", b.count(), l.Version())
	}
}

func TestACorruptCacheIsIgnored(t *testing.T) {
	f, a := newFakeRegistry(t), &applied{}
	cache := filepath.Join(t.TempDir(), "c.json")
	if err := writeFile(cache, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if newLoader(t, f, a, cache).LoadCache() || a.count() != 0 {
		t.Fatal("a corrupt cache was used")
	}
}

func TestServiceClassifiesWithTheNewThresholdsAfterApply(t *testing.T) {
	svc := New(DefaultConfig(), nil, detector.DefaultThresholds(), &capture{})
	vec := []float64{0.04, 0, 20, 0, 0, 0} // smoke 0.04: below the default warning level
	if svc.classify(vec) != detector.TierNone {
		t.Fatalf("setup: expected none, got %v", svc.classify(vec))
	}
	th := detector.DefaultThresholds()
	th.SmokeWarn = 0.03
	svc.Apply(th, nil)
	if svc.classify(vec) != detector.TierWarning {
		t.Fatalf("after Apply: got %v, want warning", svc.classify(vec))
	}
}

func TestRunPollsUntilTheContextEnds(t *testing.T) {
	f, a := newFakeRegistry(t), &applied{}
	f.set(bundleV(1, 0.04), http.StatusOK)
	l := newLoader(t, f, a, filepath.Join(t.TempDir(), "c.json"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx, 10*time.Millisecond); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for a.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if a.count() != 1 {
		t.Fatalf("applies = %d", a.count())
	}
}
