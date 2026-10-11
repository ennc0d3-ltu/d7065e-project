package detectsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/dataset"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
)

// These two tests are measurements, not checks: they print the numbers behind
// the scale section of the report (NFR13). They are slow and only run when
// SCALE=1:
//
//	SCALE=1 go test -count=1 -v -run 'TestScale|TestIngest' ./internal/detectsvc/
//
// The nominal load is the live system's: every sensor samples once per sim
// second and the simulation runs at 10x, so N rooms of 3 sensors produce
// 30*N readings per wall second, and one decision round (5 sim s) is 0.5 s of
// wall time.
func needScale(t *testing.T) {
	if os.Getenv("SCALE") == "" {
		t.Skip("set SCALE=1 to run the scale measurements")
	}
}

type nopPub struct{ n atomic.Int64 }

func (p *nopPub) Publish(context.Context, string, byte, []byte) error { p.n.Add(1); return nil }

func deployedService(t *testing.T, pub Publisher) *Service {
	t.Helper()
	train := dataset.Vectors(dataset.TrainingRuns())
	forest, err := detector.Train(train, 100, 256, 42)
	if err != nil {
		t.Fatal(err)
	}
	return New(DefaultConfig(), detector.NewModel(forest, train), detector.DefaultThresholds(), pub)
}

func reading(room, typ string, seq uint64, sim float64) []byte {
	v := map[string]float64{"smoke": 0.001, "co": 1, "temperature": 20}[typ]
	b, _ := json.Marshal(gateway.Reading{SensorID: room + "-" + typ, Room: room, Type: typ, Value: v, Seq: seq, SimTime: sim})
	return b
}

// TestScaleDecisionCost: how much CPU one decision round and one reading cost
// as the number of instrumented rooms grows, and the largest N the detector
// can serve at nominal and at 10x burst load on this machine.
func TestScaleDecisionCost(t *testing.T) {
	needScale(t)
	ctx := context.Background()
	fmt.Printf("SCALE %-6s %14s %16s %14s %14s\n", "rooms", "handle (rd/s)", "decision round", "round/budget", "max burst x")
	for _, n := range []int{10, 50, 100, 500, 1000, 2000, 5000} {
		pub := &nopPub{}
		svc := deployedService(t, pub)
		types := []string{"smoke", "co", "temperature"}
		var seq uint64
		var handled int
		var spent time.Duration
		for sim := 0.0; sim < 90; sim++ { // fill every room's feature window
			for r := 0; r < n; r++ {
				for _, typ := range types {
					seq++
					p := reading(fmt.Sprintf("level0/R%d", r), typ, seq, sim)
					t0 := time.Now()
					_ = svc.Handle(ctx, "", p)
					spent += time.Since(t0)
					handled++
				}
			}
		}
		rate := float64(handled) / spent.Seconds()
		// time decision rounds on fresh data: feed one more sim second of
		// readings before every round, otherwise the rooms are stale and the
		// round takes the cheap "no data" path
		var rounds []float64
		sim := 90.0
		for i := 0; i < 15; i++ {
			for step := 0; step < 5; step++ {
				for r := 0; r < n; r++ {
					for _, typ := range types {
						seq++
						_ = svc.Handle(ctx, "", reading(fmt.Sprintf("level0/R%d", r), typ, seq, sim))
					}
				}
				sim++
			}
			t0 := time.Now()
			svc.Tick(ctx, sim)
			rounds = append(rounds, time.Since(t0).Seconds())
		}
		sort.Float64s(rounds)
		med := rounds[len(rounds)/2]
		const budget = 0.5 // one decision period (5 sim s) at 10x
		// nominal readings per wall second is 30*n; the detector also spends the
		// round time every 0.5 s, which takes part of each second
		spare := 1 - med/budget
		maxBurst := 0.0
		if spare > 0 {
			maxBurst = rate * spare / (30 * float64(n))
		}
		fmt.Printf("SCALE %-6d %14.0f %13.1f ms %13.0f%% %14.1f\n", n, rate, med*1000, 100*med/budget, maxBurst)
	}
}

// TestIngestThroughBrokerLimit: readings for 50 rooms published through a real
// (embedded) MQTT broker into the detector's handler at increasing multiples of
// the nominal rate; reports the rate actually handled and whether a backlog
// builds.
func TestIngestThroughBrokerLimit(t *testing.T) {
	needScale(t)
	srv := mqtt.New(&mqtt.Options{InlineClient: true})
	_ = srv.AddHook(new(auth.AllowHook), nil)
	l := listeners.NewTCP(listeners.Config{ID: "scale", Address: "127.0.0.1:0"})
	if err := srv.AddListener(l); err != nil {
		t.Fatal(err)
	}
	if err := srv.Serve(); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	url := "tcp://" + l.Address()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := deployedService(t, &nopPub{})
	sub, err := mqttx.Connect(ctx, url, "detector-under-test")
	if err != nil {
		t.Fatal(err)
	}
	var handled atomic.Int64
	if err := sub.Subscribe(ctx, "sensors/#", 1, func(topic string, payload []byte) {
		_ = svc.Handle(ctx, topic, payload)
		handled.Add(1)
	}); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	var pubs []*mqttx.Conn
	for i := 0; i < workers; i++ {
		c, err := mqttx.Connect(ctx, url, fmt.Sprintf("load-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		pubs = append(pubs, c)
	}

	const rooms = 50
	nominal := 30.0 * rooms // readings per wall second at 10x
	fmt.Printf("INGEST nominal for %d rooms = %.0f readings/s\n", rooms, nominal)
	fmt.Printf("INGEST %-10s %12s %12s %12s %10s\n", "load", "target/s", "published/s", "handled/s", "backlog")
	types := []string{"smoke", "co", "temperature"}
	for _, mult := range []float64{1, 10, 30, 100, 300} {
		target := nominal * mult
		hBefore := handled.Load()
		var published atomic.Int64
		start := time.Now()
		done := make(chan struct{})
		for w := 0; w < workers; w++ {
			go func(w int) {
				defer func() { done <- struct{}{} }()
				perWorker := target / workers
				slice := int(math.Ceil(perWorker * 0.01))
				var local uint64
				for time.Since(start) < 5*time.Second {
					t0 := time.Now()
					for i := 0; i < slice; i++ {
						local++
						room := fmt.Sprintf("level0/R%d", (int(local)*workers+w)%rooms)
						typ := types[int(local)%3]
						_ = pubs[w].Publish(ctx, "sensors/"+room+"/"+typ, 1, reading(room, typ, local*workers+uint64(w), float64(local/uint64(rooms))))
						published.Add(1)
					}
					if d := 10*time.Millisecond - time.Since(t0); d > 0 {
						time.Sleep(d)
					}
				}
			}(w)
		}
		for w := 0; w < workers; w++ {
			<-done
		}
		elapsed := time.Since(start).Seconds()
		time.Sleep(500 * time.Millisecond) // let the tail drain
		h := handled.Load() - hBefore
		fmt.Printf("INGEST %-10s %12.0f %12.0f %12.0f %10d\n", fmt.Sprintf("%.0fx", mult), target, float64(published.Load())/elapsed, float64(h)/elapsed, published.Load()-h)
	}
}
