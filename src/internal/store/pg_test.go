package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/dbwriter"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// The store satisfies the writer's interface.
var _ dbwriter.Store = (*PG)(nil)
var _ dbwriter.Store = (*Reconnecting)(nil)

var testCounter atomic.Int64

// openTest connects to the integration database, or skips.
// TEST_DATABASE_URL=postgres://fire@127.0.0.1:55432/fire?sslmode=disable
func openTest(t *testing.T) *PG {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set: skipping the database integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pg, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pg.Close)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pg
}

// room returns a room key unique to this test run, so tests do not see each
// other's rows.
func room(t *testing.T) string {
	return fmt.Sprintf("level0/T%d-%d", time.Now().UnixNano(), testCounter.Add(1))
}

func count(t *testing.T, pg *PG, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pg.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrateIsIdempotentAndCreatesHypertables(t *testing.T) {
	pg := openTest(t)
	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	for _, table := range []string{"readings", "decisions", "commands"} {
		if n := count(t, pg, `SELECT count(*) FROM timescaledb_information.hypertables WHERE hypertable_name = $1`, table); n != 1 {
			t.Errorf("%s is not a hypertable", table)
		}
	}
}

func TestRetentionPolicyIsInstalledOnReadings(t *testing.T) {
	pg := openTest(t)
	n := count(t, pg, `SELECT count(*) FROM timescaledb_information.jobs WHERE proc_name = 'policy_retention' AND hypertable_name = 'readings'`)
	if n != 1 {
		t.Fatalf("retention jobs on readings = %d, want 1", n)
	}
}

func TestReadingsAreStoredWithTheirSimTime(t *testing.T) {
	pg := openTest(t)
	r := room(t)
	rs := []gateway.Reading{
		{SensorID: "s1", Room: r, Type: "smoke", Unit: "1/m", Value: 0.12, Seq: 1, SimTime: 5},
		{SensorID: "s1", Room: r, Type: "smoke", Unit: "1/m", Value: 0.34, Seq: 2, SimTime: 10},
	}
	if err := pg.InsertReadings(context.Background(), rs); err != nil {
		t.Fatal(err)
	}
	var sim, val float64
	err := pg.pool.QueryRow(context.Background(),
		`SELECT sim_time, value FROM readings WHERE room = $1 ORDER BY sim_time DESC LIMIT 1`, r).Scan(&sim, &val)
	if err != nil {
		t.Fatal(err)
	}
	if sim != 10 || val != 0.34 {
		t.Fatalf("stored sim_time=%v value=%v", sim, val)
	}
	if n := count(t, pg, `SELECT count(*) FROM readings WHERE room = $1`, r); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

func TestAnEmptyBatchIsANoOp(t *testing.T) {
	pg := openTest(t)
	ctx := context.Background()
	if err := pg.InsertReadings(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := pg.InsertDecisions(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := pg.InsertCommands(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func TestALargeBatchGoesInInOneCall(t *testing.T) {
	pg := openTest(t)
	r := room(t)
	rs := make([]gateway.Reading, 5000)
	for i := range rs {
		rs[i] = gateway.Reading{SensorID: "s", Room: r, Type: "co", Unit: "ppm", Value: float64(i), Seq: uint64(i + 1), SimTime: float64(i)}
	}
	if err := pg.InsertReadings(context.Background(), rs); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pg, `SELECT count(*) FROM readings WHERE room = $1`, r); n != 5000 {
		t.Fatalf("rows = %d, want 5000", n)
	}
}

func TestDecisionsKeepTheReasonAndStaleSignals(t *testing.T) {
	pg := openTest(t)
	r := room(t)
	d := proto.Decision{Room: r, SimTime: 20, Tier: "warning", State: "warning", Action: "warn", Reason: "armed", Stale: []string{"temperature"}}
	if err := pg.InsertDecisions(context.Background(), []proto.Decision{d}); err != nil {
		t.Fatal(err)
	}
	var reason string
	var stale []string
	err := pg.pool.QueryRow(context.Background(), `SELECT reason, stale FROM decisions WHERE room = $1`, r).Scan(&reason, &stale)
	if err != nil {
		t.Fatal(err)
	}
	if reason != "armed" || len(stale) != 1 || stale[0] != "temperature" {
		t.Fatalf("reason=%q stale=%v", reason, stale)
	}
}

func TestDecisionWithNoStaleSignalsStoresAnEmptyArray(t *testing.T) {
	pg := openTest(t)
	r := room(t)
	if err := pg.InsertDecisions(context.Background(), []proto.Decision{{Room: r, SimTime: 1, State: "idle", Action: "none"}}); err != nil {
		t.Fatal(err)
	}
	var stale []string
	if err := pg.pool.QueryRow(context.Background(), `SELECT stale FROM decisions WHERE room = $1`, r).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %v", stale)
	}
}

func TestCommandsAreStored(t *testing.T) {
	pg := openTest(t)
	r := room(t)
	c := proto.Command{Room: r, Action: proto.ActionOn, Reason: "fire tier held 15s", Seq: 7, TTL: 30, SimTime: 120}
	if err := pg.InsertCommands(context.Background(), []proto.Command{c}); err != nil {
		t.Fatal(err)
	}
	var action, reason string
	var seq int64
	var ttl float64
	err := pg.pool.QueryRow(context.Background(),
		`SELECT action, reason, seq, ttl FROM commands WHERE room = $1`, r).Scan(&action, &reason, &seq, &ttl)
	if err != nil {
		t.Fatal(err)
	}
	if action != "on" || reason != "fire tier held 15s" || seq != 7 || ttl != 30 {
		t.Fatalf("stored %q %q %d %v", action, reason, seq, ttl)
	}
}

func TestDeadLettersAreStored(t *testing.T) {
	pg := openTest(t)
	marker := room(t)
	dl := proto.DeadLetter{Process: "bridge", Topic: marker, Payload: "nope", Error: "bad payload", WallTS: "2026-10-08T00:00:00Z"}
	if err := pg.InsertDeadLetters(context.Background(), []proto.DeadLetter{dl}); err != nil {
		t.Fatal(err)
	}
	var process, payload, errText string
	err := pg.pool.QueryRow(context.Background(), `SELECT process, payload, error FROM dead_letters WHERE topic = $1`, marker).Scan(&process, &payload, &errText)
	if err != nil {
		t.Fatal(err)
	}
	if process != "bridge" || payload != "nope" || errText != "bad payload" {
		t.Fatalf("stored %q %q %q", process, payload, errText)
	}
}

func TestDispatchesAndAcknowledgementsAreStored(t *testing.T) {
	pg := openTest(t)
	inc := room(t)
	d := proto.Dispatch{Incident: inc, Kind: "fire", Severity: "critical", SimTime: 100,
		Affected: []string{"level0/A1008"},
		InDanger: []proto.PersonRef{{ID: "p1", Name: "Alice", Room: "level0/A1008"}, {ID: "p2", Name: "Bob", Room: "level0/A1008"}},
		AtRisk:   []proto.PersonRef{{ID: "p3", Name: "Carol", Room: "level0/A1024B"}}, Safe: 200,
		EnRoute: []proto.PersonRef{{ID: "p4", Name: "Dan", Room: "level0/A1008"}}, Unaccounted: []proto.PersonRef{{ID: "p5", Name: "Eve", Room: "level0/A1008"}, {ID: "p6", Name: "Fay", Room: "level0/A1008"}},
		Routes: []proto.RouteMsg{{From: "level0/A1008", To: "level0/A1016", Distance: 166}}, NoRoute: []string{}}
	a := proto.DispatchAck{Incident: inc, Responder: "mock-responder", SimTime: 110}
	if err := pg.InsertDispatches(context.Background(), []proto.Dispatch{d}, []proto.DispatchAck{a}); err != nil {
		t.Fatal(err)
	}
	var kind string
	var inDanger, atRisk, safe int
	var affected []string
	err := pg.pool.QueryRow(context.Background(),
		`SELECT kind, in_danger, at_risk, safe, affected FROM dispatches WHERE incident = $1`, inc).Scan(&kind, &inDanger, &atRisk, &safe, &affected)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "fire" || inDanger != 2 || atRisk != 1 || safe != 200 || len(affected) != 1 {
		t.Fatalf("stored kind=%q in_danger=%d at_risk=%d safe=%d affected=%v", kind, inDanger, atRisk, safe, affected)
	}
	var enRoute, unaccounted int
	if err := pg.pool.QueryRow(context.Background(), `SELECT en_route, unaccounted FROM dispatches WHERE incident = $1`, inc).Scan(&enRoute, &unaccounted); err != nil {
		t.Fatal(err)
	}
	if enRoute != 1 || unaccounted != 2 {
		t.Fatalf("stored en_route=%d unaccounted=%d, want 1 and 2", enRoute, unaccounted)
	}
	if n := count(t, pg, `SELECT count(*) FROM dispatch_acks WHERE incident = $1`, inc); n != 1 {
		t.Fatalf("acks = %d", n)
	}
}

func TestComponentStatusKeepsOneRowPerProcessWithTheNewestBeat(t *testing.T) {
	pg := openTest(t)
	name := room(t)
	ctx := context.Background()
	if err := pg.InsertHeartbeats(ctx, []proto.Heartbeat{{Process: name, Seq: 1, SimTime: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := pg.InsertHeartbeats(ctx, []proto.Heartbeat{{Process: name, Seq: 5, SimTime: 50}}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pg, `SELECT count(*) FROM component_status WHERE process = $1`, name); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	var seq int64
	var sim float64
	if err := pg.pool.QueryRow(ctx, `SELECT seq, sim_time FROM component_status WHERE process = $1`, name).Scan(&seq, &sim); err != nil {
		t.Fatal(err)
	}
	if seq != 5 || sim != 50 {
		t.Fatalf("stored seq=%d sim=%v", seq, sim)
	}
}

func TestOpenFailsFastWhenTheDatabaseIsUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Open(ctx, "postgres://fire@127.0.0.1:1/fire?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Open took %v", time.Since(start))
	}
}

// --- Reconnecting: the writer must start and buffer while the database is down ---

type fakeInner struct{ readings int }

func (f *fakeInner) InsertReadings(_ context.Context, rs []gateway.Reading) error {
	f.readings += len(rs)
	return nil
}
func (f *fakeInner) InsertDecisions(context.Context, []proto.Decision) error { return nil }
func (f *fakeInner) InsertCommands(context.Context, []proto.Command) error   { return nil }
func (f *fakeInner) InsertDeadLetters(context.Context, []proto.DeadLetter) error {
	return nil
}
func (f *fakeInner) InsertDispatches(context.Context, []proto.Dispatch, []proto.DispatchAck) error {
	return nil
}
func (f *fakeInner) InsertHeartbeats(context.Context, []proto.Heartbeat) error { return nil }

func TestReconnectingReportsUnavailableUntilTheDatabaseComesUp(t *testing.T) {
	up := false
	inner := &fakeInner{}
	opens := 0
	rc := NewReconnecting(func(context.Context) (dbwriter.Store, error) {
		opens++
		if !up {
			return nil, errors.New("connection refused")
		}
		return inner, nil
	}, 0) // no minimum wait between attempts
	ctx := context.Background()
	err := rc.InsertReadings(ctx, []gateway.Reading{{SensorID: "s"}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	up = true
	if err := rc.InsertReadings(ctx, []gateway.Reading{{SensorID: "s"}, {SensorID: "s"}}); err != nil {
		t.Fatalf("after the database came up: %v", err)
	}
	if inner.readings != 2 || opens != 2 {
		t.Fatalf("readings=%d opens=%d", inner.readings, opens)
	}
}

func TestReconnectingDoesNotHammerADeadDatabase(t *testing.T) {
	opens := 0
	rc := NewReconnecting(func(context.Context) (dbwriter.Store, error) {
		opens++
		return nil, errors.New("down")
	}, time.Hour)
	for i := 0; i < 20; i++ {
		_ = rc.InsertReadings(context.Background(), []gateway.Reading{{}})
	}
	if opens != 1 {
		t.Fatalf("opened %d times inside the retry interval, want 1", opens)
	}
}
