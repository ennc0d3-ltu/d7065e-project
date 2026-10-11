package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeTruth struct {
	byRoom map[string]Truth
	calls  map[string]int
}

func (f *fakeTruth) Truth(_ context.Context, room string) (Truth, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[room]++
	tr, ok := f.byRoom[room]
	if !ok {
		return Truth{}, errors.New("no truth for " + room)
	}
	return tr, nil
}

type captureSink struct {
	got     []Reading
	failing int // fail the next n publishes
}

func (c *captureSink) Publish(_ context.Context, r Reading) error {
	if c.failing > 0 {
		c.failing--
		return errors.New("broker down")
	}
	c.got = append(c.got, r)
	return nil
}

func specs() []SensorSpec {
	return []SensorSpec{
		{ID: "A109-temp", Room: "level0/A109", Kind: KindTemperature},
		{ID: "A109-smoke", Room: "level0/A109", Kind: KindSmoke},
		{ID: "A109-co", Room: "level0/A109", Kind: KindCO},
		{ID: "A108-temp", Room: "level0/A108", Kind: KindTemperature},
	}
}

func newGW(src *fakeTruth, sink *captureSink) *Gateway {
	return New(specs(), src, sink)
}

func truthAt(t float64) Truth { return Truth{TempC: 30, Smoke: 0.2, COppm: 55, SimTime: t} }

func TestSampleReadsTheRightFieldForEachSensorKind(t *testing.T) {
	src := &fakeTruth{byRoom: map[string]Truth{"level0/A109": truthAt(10), "level0/A108": truthAt(10)}}
	sink := &captureSink{}
	pub, errs := newGW(src, sink).Sample(context.Background())
	if pub != 4 || errs != 0 {
		t.Fatalf("published=%d errs=%d", pub, errs)
	}
	want := map[string]struct {
		typ, unit string
		val       float64
	}{
		"A109-temp":  {"temperature", "°C", 30},
		"A109-smoke": {"smoke", "1/m", 0.2},
		"A109-co":    {"co", "ppm", 55},
		"A108-temp":  {"temperature", "°C", 30},
	}
	for _, r := range sink.got {
		w := want[r.SensorID]
		if r.Type != w.typ || r.Unit != w.unit || r.Value != w.val || r.SimTime != 10 {
			t.Errorf("%s: %+v, want %+v", r.SensorID, r, w)
		}
	}
}

func TestNoisyReadingsOfSmokeAndCONeverGoNegative(t *testing.T) {
	// Truth is zero and the noise is large: an unclamped sensor would report
	// negative smoke or CO about half the time, which no real sensor does.
	spec := []SensorSpec{
		{ID: "s", Room: "level0/A109", Kind: KindSmoke, Sigma: 1, Seed: 3},
		{ID: "c", Room: "level0/A109", Kind: KindCO, Sigma: 10, Seed: 4},
	}
	src := &fakeTruth{byRoom: map[string]Truth{"level0/A109": {SimTime: 1}}}
	sink := &captureSink{}
	g := New(spec, src, sink)
	for i := 0; i < 200; i++ {
		g.Sample(context.Background())
	}
	for _, r := range sink.got {
		if r.Value < 0 {
			t.Fatalf("%s reported a negative %s: %v", r.SensorID, r.Type, r.Value)
		}
	}
}

func TestTemperatureMayGoBelowZero(t *testing.T) {
	spec := []SensorSpec{{ID: "t", Room: "level0/A109", Kind: KindTemperature}}
	src := &fakeTruth{byRoom: map[string]Truth{"level0/A109": {TempC: -5, SimTime: 1}}}
	sink := &captureSink{}
	New(spec, src, sink).Sample(context.Background())
	if len(sink.got) != 1 || sink.got[0].Value != -5 {
		t.Fatalf("temperature must not be clamped: %+v", sink.got)
	}
}

func TestEachRoomsTruthIsFetchedOncePerSample(t *testing.T) {
	src := &fakeTruth{byRoom: map[string]Truth{"level0/A109": truthAt(1), "level0/A108": truthAt(1)}}
	newGW(src, &captureSink{}).Sample(context.Background())
	if src.calls["level0/A109"] != 1 {
		t.Fatalf("A109 truth fetched %d times for 3 sensors, want 1", src.calls["level0/A109"])
	}
}

func TestSequenceNumbersCountUpPerSensor(t *testing.T) {
	src := &fakeTruth{byRoom: map[string]Truth{"level0/A109": truthAt(1), "level0/A108": truthAt(1)}}
	sink := &captureSink{}
	g := newGW(src, sink)
	for i := 0; i < 3; i++ {
		g.Sample(context.Background())
	}
	var last uint64
	for _, r := range sink.got {
		if r.SensorID == "A109-temp" {
			last = r.Seq
		}
	}
	if last != 3 {
		t.Fatalf("A109-temp last seq = %d, want 3", last)
	}
}

func TestTruthFailureSilencesOnlyThatRoom(t *testing.T) {
	src := &fakeTruth{byRoom: map[string]Truth{"level0/A108": truthAt(1)}} // A109 missing
	sink := &captureSink{}
	pub, errs := newGW(src, sink).Sample(context.Background())
	if pub != 1 || errs != 1 {
		t.Fatalf("published=%d errs=%d, want 1 and 1", pub, errs)
	}
	if sink.got[0].SensorID != "A108-temp" {
		t.Fatalf("wrong sensor survived: %+v", sink.got[0])
	}
}

func TestFailedPublishLeavesASequenceGap(t *testing.T) {
	src := &fakeTruth{byRoom: map[string]Truth{"level0/A109": truthAt(1), "level0/A108": truthAt(1)}}
	sink := &captureSink{failing: 4} // first Sample: every publish fails
	g := newGW(src, sink)
	g.Sample(context.Background())
	g.Sample(context.Background())
	for _, r := range sink.got {
		if r.Seq != 2 {
			t.Fatalf("%s seq = %d, want 2 (seq 1 was lost, so receivers can see the gap)", r.SensorID, r.Seq)
		}
	}
}

func TestOfflineSensorStopsPublishingOthersContinue(t *testing.T) {
	src := &fakeTruth{byRoom: map[string]Truth{"level0/A109": truthAt(1), "level0/A108": truthAt(1)}}
	sink := &captureSink{}
	g := newGW(src, sink)
	if err := g.SetFault("A109-smoke", FaultOffline, 0); err != nil {
		t.Fatal(err)
	}
	pub, _ := g.Sample(context.Background())
	if pub != 3 {
		t.Fatalf("published %d, want 3", pub)
	}
	for _, r := range sink.got {
		if r.SensorID == "A109-smoke" {
			t.Fatal("offline sensor published")
		}
	}
}

func TestSetFaultOnUnknownSensorFails(t *testing.T) {
	g := newGW(&fakeTruth{}, &captureSink{})
	if err := g.SetFault("nope", FaultStuck, 0); !errors.Is(err, ErrUnknownSensor) {
		t.Fatalf("err = %v, want ErrUnknownSensor", err)
	}
}

func post(t *testing.T, h http.Handler, path, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestFaultEndpointInjectsAndClearsFaults(t *testing.T) {
	src := &fakeTruth{byRoom: map[string]Truth{"level0/A109": truthAt(1), "level0/A108": truthAt(1)}}
	sink := &captureSink{}
	g := newGW(src, sink)
	h := g.FaultHandler()

	if code := post(t, h, "/faults/A108-temp", `{"mode":"offline"}`); code != http.StatusNoContent {
		t.Fatalf("inject = %d", code)
	}
	if pub, _ := g.Sample(context.Background()); pub != 3 {
		t.Fatalf("after offline fault published %d, want 3", pub)
	}
	if code := post(t, h, "/faults/A108-temp", `{"mode":"none"}`); code != http.StatusNoContent {
		t.Fatalf("clear = %d", code)
	}
	if pub, _ := g.Sample(context.Background()); pub != 4 {
		t.Fatalf("after clearing published %d, want 4", pub)
	}
}

func TestFaultEndpointRejectsBadInput(t *testing.T) {
	h := newGW(&fakeTruth{}, &captureSink{}).FaultHandler()
	if code := post(t, h, "/faults/nope", `{"mode":"stuck"}`); code != http.StatusNotFound {
		t.Errorf("unknown sensor = %d, want 404", code)
	}
	if code := post(t, h, "/faults/A109-temp", `{"mode":"explode"}`); code != http.StatusBadRequest {
		t.Errorf("bad mode = %d, want 400", code)
	}
	if code := post(t, h, "/faults/A109-temp", `not json`); code != http.StatusBadRequest {
		t.Errorf("bad body = %d, want 400", code)
	}
}

func TestTruthClientParsesTheTruthEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/truth/rooms/level0/A109" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"room":"level0/A109","temp_c":41.5,"smoke":0.3,"co_ppm":120,"sim_time":77}`))
	}))
	defer srv.Close()
	got, err := NewTruthClient(srv.URL).Truth(context.Background(), "level0/A109")
	if err != nil {
		t.Fatal(err)
	}
	if got.TempC != 41.5 || got.Smoke != 0.3 || got.COppm != 120 || got.SimTime != 77 {
		t.Fatalf("truth = %+v", got)
	}
	if _, err := NewTruthClient(srv.URL).Truth(context.Background(), "level0/NOPE"); err == nil {
		t.Fatal("expected an error for an unknown room")
	}
}
