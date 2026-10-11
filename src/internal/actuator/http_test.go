package actuator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
)

func post(h http.Handler, body string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/readings", strings.NewReader(body)))
	return rec.Code
}

func TestReadingsEndpointDeliversAValidReading(t *testing.T) {
	var got []gateway.Reading
	h := ReadingsHandler(func(r gateway.Reading) { got = append(got, r) })
	if code := post(h, `{"sensor_id":"s","room":"level0/A109","type":"co","value":5,"seq":1,"sim_time":10}`); code != http.StatusNoContent {
		t.Fatalf("code = %d", code)
	}
	if len(got) != 1 || got[0].Room != "level0/A109" || got[0].SimTime != 10 {
		t.Fatalf("got = %+v", got)
	}
}

func TestReadingsEndpointRejectsBadInputWithoutCallingBack(t *testing.T) {
	called := false
	h := ReadingsHandler(func(gateway.Reading) { called = true })
	for _, body := range []string{`nope`, `{"type":"co","value":1}`, `{"room":"level0/A","value":1}`} {
		if code := post(h, body); code != http.StatusBadRequest {
			t.Errorf("%q = %d, want 400", body, code)
		}
	}
	if called {
		t.Fatal("a rejected reading reached the controller")
	}
}

func TestReadingsEndpointOnlyAcceptsPost(t *testing.T) {
	h := ReadingsHandler(func(gateway.Reading) {})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readings", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", rec.Code)
	}
}
