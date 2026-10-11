package registry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
)

func bundle(note string) Bundle {
	return Bundle{Thresholds: detector.DefaultThresholds(), Note: note, Author: "tuner"}
}

func newServer(t *testing.T) (*Store, http.Handler) {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st, Handler(st)
}

func call(h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var b strings.Builder
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(b.String())))
	return rec
}

func TestAPublishedBundleIsACandidateAndNeverActiveByItself(t *testing.T) {
	st, _ := newServer(t)
	v, err := st.Publish(bundle("first"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Get(v); got.Status != Candidate {
		t.Fatalf("status = %s, want candidate", got.Status)
	}
	if _, ok := st.Active(); ok {
		t.Fatal("publishing activated a bundle: promotion needs a human")
	}
}

func TestPublishRejectsThresholdsLooserThanTheHardLimits(t *testing.T) {
	st, _ := newServer(t)
	b := bundle("blind")
	b.Thresholds.COFire = detector.MaxCOFire + 100
	if _, err := st.Publish(b); err == nil {
		t.Fatal("a bundle that breaks the hard limits was stored")
	}
}

func TestPublishRejectsAModelThatDoesNotLoad(t *testing.T) {
	st, _ := newServer(t)
	b := bundle("bad model")
	b.Model = json.RawMessage(`{"not":"a model"}`)
	if _, err := st.Publish(b); err == nil {
		t.Fatal("an unloadable model was stored")
	}
}

func TestPromoteMakesItActiveAndRetiresThePrevious(t *testing.T) {
	st, _ := newServer(t)
	v1, _ := st.Publish(bundle("one"))
	v2, _ := st.Publish(bundle("two"))
	if err := st.Promote(v1, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := st.Promote(v2, "alice"); err != nil {
		t.Fatal(err)
	}
	a, _ := st.Active()
	if a.Version != v2 {
		t.Fatalf("active = %d, want %d", a.Version, v2)
	}
	if old, _ := st.Get(v1); old.Status != Retired {
		t.Fatalf("previous = %s, want retired", old.Status)
	}
	if a.PromotedBy != "alice" {
		t.Fatal("who promoted it was not recorded")
	}
}

func TestPromoteNeedsAnApprover(t *testing.T) {
	st, _ := newServer(t)
	v, _ := st.Publish(bundle("x"))
	if err := st.Promote(v, ""); err == nil {
		t.Fatal("promoted with nobody accountable")
	}
}

func TestRollbackReactivatesThePreviousActiveBundle(t *testing.T) {
	st, _ := newServer(t)
	v1, _ := st.Publish(bundle("one"))
	v2, _ := st.Publish(bundle("two"))
	_ = st.Promote(v1, "a")
	_ = st.Promote(v2, "a")
	if err := st.Rollback("a"); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.Active(); a.Version != v1 {
		t.Fatalf("after rollback active = %d, want %d", a.Version, v1)
	}
	if bad, _ := st.Get(v2); bad.Status != Retired {
		t.Fatalf("rolled-back bundle = %s, want retired", bad.Status)
	}
}

func TestRollbackWithNothingToGoBackToIsAnError(t *testing.T) {
	st, _ := newServer(t)
	v, _ := st.Publish(bundle("only"))
	_ = st.Promote(v, "a")
	if err := st.Rollback("a"); err == nil {
		t.Fatal("rolled back to nothing")
	}
}

func TestStateSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	st, _ := OpenStore(path)
	v, _ := st.Publish(bundle("persist"))
	_ = st.Promote(v, "a")
	again, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := again.Active(); !ok || a.Version != v {
		t.Fatalf("active after restart = %+v", a)
	}
}

func TestHTTPFlow(t *testing.T) {
	_, h := newServer(t)
	if rec := call(h, http.MethodGet, "/active", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no active bundle: code = %d, want 404", rec.Code)
	}
	rec := call(h, http.MethodPost, "/bundles", bundle("via http"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", rec.Code, rec.Body)
	}
	var out struct{ Version int }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec := call(h, http.MethodPost, "/bundles/1/promote", map[string]string{"by": "bob"}); rec.Code != http.StatusNoContent {
		t.Fatalf("promote = %d %s", rec.Code, rec.Body)
	}
	rec = call(h, http.MethodGet, "/active", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Version":1`) {
		t.Fatalf("active = %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, http.MethodPost, "/bundles/99/promote", map[string]string{"by": "bob"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown version = %d", rec.Code)
	}
}

func TestAnEmptyRegistryListsAsAnEmptyArrayNotNull(t *testing.T) {
	_, h := newServer(t)
	if body := strings.TrimSpace(call(h, http.MethodGet, "/bundles", nil).Body.String()); body != "[]" {
		t.Fatalf("body = %q, want []", body)
	}
}
