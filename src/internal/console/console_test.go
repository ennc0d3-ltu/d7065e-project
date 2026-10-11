package console

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// upstream records what it was asked and answers with a fixed body.
type upstream struct {
	srv    *httptest.Server
	method string
	path   string
	body   string
}

func newUpstream(t *testing.T, status int, reply string) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.method, u.path, u.body = r.Method, r.URL.Path, string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestThePageIsServedAndTalksToTheConsoleAPI(t *testing.T) {
	h := Handler(newUpstream(t, 200, `{}`).srv.URL, newUpstream(t, 200, `{}`).srv.URL, Links{})
	rec := do(h, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("code=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{"/physics/fire", "/physics/clear", "/physics/state", "/detector/"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the page never calls %s", want)
		}
	}
}

func TestSimulatorRequestsAreForwardedToThePhysicsControlAPI(t *testing.T) {
	phys := newUpstream(t, http.StatusAccepted, ``)
	h := Handler(phys.srv.URL, newUpstream(t, 200, `{}`).srv.URL, Links{})
	rec := do(h, http.MethodPost, "/physics/fire", `{"room":"level0/A1008","growth":"fast"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d", rec.Code)
	}
	if phys.method != http.MethodPost || phys.path != "/control/fire" || !strings.Contains(phys.body, "A1008") {
		t.Fatalf("upstream saw %s %s %q", phys.method, phys.path, phys.body)
	}
}

func TestStateIsReadFromPhysics(t *testing.T) {
	phys := newUpstream(t, 200, `{"sim_time":42,"rooms":["level0/A1008"],"active":[]}`)
	h := Handler(phys.srv.URL, newUpstream(t, 200, `[]`).srv.URL, Links{})
	rec := do(h, http.MethodGet, "/physics/state", "")
	if phys.path != "/control/state" || !strings.Contains(rec.Body.String(), `"sim_time":42`) {
		t.Fatalf("path=%q body=%q", phys.path, rec.Body)
	}
}

func TestOperatorActionsAreForwardedToTheDetector(t *testing.T) {
	det := newUpstream(t, http.StatusNoContent, ``)
	h := Handler(newUpstream(t, 200, `{}`).srv.URL, det.srv.URL, Links{})
	rec := do(h, http.MethodPost, "/detector/operator/level0/A1008/confirm", `{"verdict":"fire"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rec.Code)
	}
	if det.path != "/operator/level0/A1008/confirm" || !strings.Contains(det.body, "fire") {
		t.Fatalf("detector saw %s %q", det.path, det.body)
	}
}

func TestDetectorStatusIsReadThroughTheConsole(t *testing.T) {
	det := newUpstream(t, 200, `[{"Room":"level0/A1008","State":"fire"}]`)
	h := Handler(newUpstream(t, 200, `{}`).srv.URL, det.srv.URL, Links{})
	rec := do(h, http.MethodGet, "/detector/status", "")
	if det.path != "/status" || !strings.Contains(rec.Body.String(), "fire") {
		t.Fatalf("path=%q body=%q", det.path, rec.Body)
	}
}

func TestAnUnreachableUpstreamIsABadGatewayNotACrash(t *testing.T) {
	h := Handler("http://127.0.0.1:1", "http://127.0.0.1:1", Links{})
	for _, p := range []string{"/physics/state", "/detector/status"} {
		if rec := do(h, http.MethodGet, p, ""); rec.Code != http.StatusBadGateway {
			t.Errorf("%s = %d, want 502", p, rec.Code)
		}
	}
}

func TestOnlyTheKnownEndpointsAreProxied(t *testing.T) {
	// the console must not become an open proxy into the physics or detector API
	phys := newUpstream(t, 200, `{}`)
	det := newUpstream(t, 200, `{}`)
	h := Handler(phys.srv.URL, det.srv.URL, Links{})
	for _, p := range []string{
		"/physics/../healthz", "/physics/anything-else", "/detector/healthz",
		"/detector/operator/../../x", "/healthz", "/control/fire", "/operator/level0/A/confirm",
	} {
		rec := do(h, http.MethodGet, p, "")
		// the router may redirect a messy path to its clean form (307); what it
		// must never do is answer from, or pass anything on to, an upstream
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusTemporaryRedirect && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d", p, rec.Code)
		}
	}
	if phys.path != "" || det.path != "" {
		t.Fatalf("an unknown path reached an upstream: physics %q detector %q", phys.path, det.path)
	}
}

func TestAnOperatorActionOtherThanTheKnownOnesIsStillOnlyForwardedAsAPost(t *testing.T) {
	det := newUpstream(t, 400, `bad action`)
	h := Handler(newUpstream(t, 200, `{}`).srv.URL, det.srv.URL, Links{})
	if rec := do(h, http.MethodGet, "/detector/operator/level0/A1008/confirm", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on an operator action = %d, want 405", rec.Code)
	}
	if det.path != "" {
		t.Fatal("a GET reached the detector's operator API")
	}
}

func TestLinksToTheViewerAndGrafanaAppearOnThePage(t *testing.T) {
	h := Handler(newUpstream(t, 200, `{}`).srv.URL, newUpstream(t, 200, `{}`).srv.URL,
		Links{Viewer: "http://127.0.0.1:9090/?floor=level0&layer=fire-risk", Grafana: "http://127.0.0.1:3000"})
	body := do(h, http.MethodGet, "/", "").Body.String()
	if !strings.Contains(body, "127.0.0.1:9090") || !strings.Contains(body, "127.0.0.1:3000") {
		t.Fatal("the page does not link to the viewer and Grafana")
	}
}
