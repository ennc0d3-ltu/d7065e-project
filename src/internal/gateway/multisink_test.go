package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type flaky struct {
	fail bool
	got  []Reading
}

func (f *flaky) Publish(_ context.Context, r Reading) error {
	if f.fail {
		return errors.New("down")
	}
	f.got = append(f.got, r)
	return nil
}

func TestMultiSinkDeliversToEveryPath(t *testing.T) {
	a, b := &flaky{}, &flaky{}
	if err := NewMultiSink(a, b).Publish(context.Background(), Reading{SensorID: "s", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if len(a.got) != 1 || len(b.got) != 1 {
		t.Fatalf("a=%d b=%d", len(a.got), len(b.got))
	}
}

func TestMultiSinkSucceedsWhileAnyPathWorks(t *testing.T) {
	broken, ok := &flaky{fail: true}, &flaky{}
	if err := NewMultiSink(broken, ok).Publish(context.Background(), Reading{Seq: 1}); err != nil {
		t.Fatalf("one working path must be enough: %v", err)
	}
	if len(ok.got) != 1 {
		t.Fatal("the working path was skipped after the other failed")
	}
}

func TestMultiSinkFailsOnlyWhenEveryPathFails(t *testing.T) {
	if err := NewMultiSink(&flaky{fail: true}, &flaky{fail: true}).Publish(context.Background(), Reading{}); err == nil {
		t.Fatal("expected an error when all paths fail")
	}
}

func TestHTTPSinkPostsTheReadingAsJSON(t *testing.T) {
	var got Reading
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	want := Reading{SensorID: "s", Room: "level0/A109", Type: "co", Unit: "ppm", Value: 12, Seq: 3, SimTime: 40}
	if err := NewHTTPSink(srv.URL).Publish(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/readings" || got != want {
		t.Fatalf("%s %s %+v", method, path, got)
	}
}

func TestHTTPSinkReportsAnErrorStatusAndAnUnreachableServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", 500) }))
	defer srv.Close()
	if err := NewHTTPSink(srv.URL).Publish(context.Background(), Reading{}); err == nil {
		t.Error("a 500 was treated as success")
	}
	if err := NewHTTPSink("http://127.0.0.1:1").Publish(context.Background(), Reading{}); err == nil {
		t.Error("an unreachable server was treated as success")
	}
}
