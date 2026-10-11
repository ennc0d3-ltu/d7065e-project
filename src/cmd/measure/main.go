// measure runs the timing experiments against a running docker compose stack
// and prints the numbers the report cites.
//
//	measure latency  [-runs 20] [-room level0/1552A] [-growth fast] [-speed 10]
//	measure recovery [-services detector,sprinkler-actuator,...]
//
// latency (NFR1): for each run it resets the room, starts a fire, and records
//   - the first sample the deployed classifier calls fire,
//   - the moment the fire tier has held for the escalation window (the
//     "criteria" are then met),
//   - the first sprinkler "on" command on MQTT,
//   - the moment BuildSim reports the sprinkler on (polled every 20 ms).
//
// recovery (NFR4): SIGKILLs each container and times how long until it answers
// /readyz again and until its first heartbeat reaches the broker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/actuator"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/dataset"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: measure latency|recovery [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "latency":
		latency(args)
	case "recovery":
		recovery(args)
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(2)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func post(url, body string) error {
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return nil
}

// median is the middle value, or the mean of the two middle values for an even count.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 0 {
		return (s[n/2-1] + s[n/2]) / 2
	}
	return s[len(s)/2]
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

// ---------------------------------------------------------------- latency

type runState struct {
	mu        sync.Mutex
	feat      *detector.RoomFeatures
	classify  func([]float64) detector.Tier
	hold      float64
	fireSince float64 // sim time the fire tier began holding; -1 when not
	fireWall  time.Time
	fireSim   float64
	critWall  time.Time
	critSim   float64
	cmdWall   time.Time
}

func (s *runState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feat = detector.NewRoomFeatures(60, 15)
	s.fireSince = -1
	s.fireWall, s.critWall, s.cmdWall = time.Time{}, time.Time{}, time.Time{}
}

func (s *runState) onReading(payload []byte) {
	var rd gateway.Reading
	if json.Unmarshal(payload, &rd) != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feat.Add(rd)
	vec, _ := s.feat.VectorDegraded(rd.SimTime)
	if vec == nil || s.classify(vec) != detector.TierFire {
		s.fireSince = -1
		return
	}
	if s.fireSince < 0 {
		s.fireSince = rd.SimTime
		if s.fireWall.IsZero() {
			s.fireWall, s.fireSim = now, rd.SimTime
		}
	}
	if s.critWall.IsZero() && rd.SimTime-s.fireSince >= s.hold {
		s.critWall, s.critSim = now, rd.SimTime
	}
}

func latency(args []string) {
	fs := flag.NewFlagSet("latency", flag.ExitOnError)
	runs := fs.Int("runs", 20, "number of runs")
	room := fs.String("room", "level0/1552A", "room to ignite")
	growth := fs.String("growth", "fast", "slow, medium or fast (comma list alternates)")
	speed := fs.Float64("speed", 10, "sim seconds per wall second")
	_ = fs.Parse(args)

	physURL := env("PHYSICS_URL", "http://127.0.0.1:8090")
	detURL := env("DETECTOR_URL", "http://127.0.0.1:8093")
	bs := buildsim.New(env("BUILDSIM_URL", "http://127.0.0.1:9090"), 2*time.Second)
	spr := actuator.SprinklerID(*room)
	level, name, _ := strings.Cut(*room, "/")

	// the same classifier the detector deploys (the trainer is deterministic)
	train := dataset.Vectors(dataset.TrainingRuns())
	forest, err := detector.Train(train, 100, 256, 42)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	th := detector.DefaultThresholds()
	st := &runState{classify: detector.Deployed(th, detector.NewModel(forest, train)), hold: detector.DefaultConfig().FirePersist}
	st.reset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := mqttx.Connect(ctx, env("MQTT_URL", "tcp://127.0.0.1:1883"), "measure-latency")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mqtt:", err)
		os.Exit(1)
	}
	must(conn.Subscribe(ctx, "sensors/"+*room+"/#", 0, func(_ string, p []byte) { st.onReading(p) }))
	must(conn.Subscribe(ctx, proto.CommandTopic(*room), 0, func(_ string, p []byte) {
		var c proto.Command
		if json.Unmarshal(p, &c) == nil && c.Action == "on" {
			st.mu.Lock()
			if st.cmdWall.IsZero() {
				st.cmdWall = time.Now()
			}
			st.mu.Unlock()
		}
	}))
	classes := strings.Split(*growth, ",")

	var detectS, criteriaToOn, cmdToOn, totalS []float64
	fmt.Printf("%-4s %-7s %12s %14s %14s %12s\n", "run", "growth", "ign->fire(s)", "crit->on(s)", "cmd->on(s)", "fire->on(s)")
	for i := 0; i < *runs; i++ {
		g := classes[i%len(classes)]
		prepare(physURL, detURL, bs, spr, level, name)
		st.reset()
		time.Sleep(500 * time.Millisecond)

		ignite := time.Now()
		if err := post(physURL+"/control/fire", fmt.Sprintf(`{"room":%q,"growth":%q}`, *room, g)); err != nil {
			fmt.Fprintln(os.Stderr, "ignite:", err)
			os.Exit(1)
		}
		var on time.Time
		deadline := time.Now().Add(150 * time.Second)
		for time.Now().Before(deadline) {
			if s, err := bs.GetActuatorState(ctx, spr); err == nil && s == "on" {
				on = time.Now()
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if on.IsZero() {
			fmt.Printf("%-4d %-7s  sprinkler never came on within 150 s\n", i+1, g)
			continue
		}
		time.Sleep(300 * time.Millisecond)
		st.mu.Lock()
		fw, cw, mw := st.fireWall, st.critWall, st.cmdWall
		st.mu.Unlock()
		sim := func(d time.Duration) float64 { return d.Seconds() * *speed }
		if fw.IsZero() || cw.IsZero() || mw.IsZero() {
			fmt.Printf("%-4d %-7s  missing a timestamp (fire sample %v, criteria %v, command %v)\n", i+1, g, !fw.IsZero(), !cw.IsZero(), !mw.IsZero())
			continue
		}
		d1, d2, d3, d4 := sim(fw.Sub(ignite)), sim(on.Sub(cw)), sim(on.Sub(mw)), sim(on.Sub(fw))
		detectS, criteriaToOn, cmdToOn, totalS = append(detectS, d1), append(criteriaToOn, d2), append(cmdToOn, d3), append(totalS, d4)
		fmt.Printf("%-4d %-7s %12.1f %14.1f %14.1f %12.1f\n", i+1, g, d1, d2, d3, d4)
	}
	cleanup(physURL, detURL, level, name)
	fmt.Printf("\nsim seconds (wall x %.0f), %d successful runs\n", *speed, len(totalS))
	row := func(label string, xs []float64) {
		fmt.Printf("%-34s median %6.1f   p95 %6.1f   max %6.1f\n", label, median(xs), percentile(xs, .95), percentile(xs, 1))
	}
	row("ignition -> first fire-tier sample", detectS)
	row("fire sample -> sprinkler on (total)", totalS)
	row("criteria met -> sprinkler on", criteriaToOn)
	row("on command -> sprinkler on", cmdToOn)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func cleanup(physURL, detURL, level, name string) {
	_ = post(physURL+"/control/reset", "")
	_ = post(fmt.Sprintf("%s/operator/%s/%s/release", detURL, level, name), "{}")
}

// prepare returns the room to a quiet state: no fire, sprinkler off, readings
// back to normal. Sensor lag (30 sim s) and the feature window (60 sim s) both
// need to flush, which takes several wall seconds at 10x.
func prepare(physURL, detURL string, bs *buildsim.Client, spr, level, name string) {
	cleanup(physURL, detURL, level, name)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := bs.GetActuatorState(context.Background(), spr); err == nil && s == "off" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(12 * time.Second)
}

// ---------------------------------------------------------------- recovery

var servicePorts = map[string]string{
	"sensor-smoke": "8091", "sensor-co": "8091", "sensor-temperature": "8091",
	"bridge": "8092", "detector": "8093", "sprinkler-actuator": "8094", "door-actuator": "8102", "dashboard": "8095",
	"responder": "8096", "db-writer": "8097", "evac-proxy": "8098", "scenario-console": "8099",
	"registry": "8100", "physics-sim": "8090",
}

func recovery(args []string) {
	fs := flag.NewFlagSet("recovery", flag.ExitOnError)
	list := fs.String("services", "sensor-smoke,sensor-co,sensor-temperature,bridge,detector,sprinkler-actuator,responder,dashboard,db-writer,evac-proxy,registry,physics-sim", "services to kill")
	project := fs.String("project", "d7065e-project", "compose project name (the directory name)")
	_ = fs.Parse(args)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := mqttx.Connect(ctx, env("MQTT_URL", "tcp://127.0.0.1:1883"), "measure-recovery")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mqtt:", err)
		os.Exit(1)
	}
	var mu sync.Mutex
	beat := map[string]time.Time{}
	must(conn.Subscribe(ctx, "heartbeat/#", 0, func(_ string, p []byte) {
		var hb proto.Heartbeat
		if json.Unmarshal(p, &hb) == nil {
			mu.Lock()
			beat[hb.Process] = time.Now()
			mu.Unlock()
		}
	}))
	time.Sleep(3 * time.Second)

	fmt.Printf("%-20s %12s %18s\n", "service", "ready (s)", "first heartbeat (s)")
	for _, svc := range strings.Split(*list, ",") {
		container := fmt.Sprintf("%s-%s-1", *project, svc)
		port := servicePorts[svc]
		if err := exec.Command("docker", "kill", container).Run(); err != nil {
			fmt.Printf("%-20s kill failed: %v\n", svc, err)
			continue
		}
		t0 := time.Now()
		// "docker kill" counts as a manual stop, so Docker's restart policy does
		// not act on it; start the container again straight away, as a
		// supervisor would, and time from the kill.
		if err := exec.Command("docker", "start", container).Run(); err != nil {
			fmt.Printf("%-20s start failed: %v\n", svc, err)
			continue
		}
		var ready, hb time.Duration
		for time.Since(t0) < 60*time.Second && (ready == 0 || (hb == 0 && hasHeartbeat(svc))) {
			if ready == 0 {
				out, err := exec.Command("docker", "exec", container, "wget", "-qO-", "-T", "1", "http://127.0.0.1:"+port+"/readyz").CombinedOutput()
				if err == nil && !strings.Contains(string(out), "not ready") {
					ready = time.Since(t0)
				}
			}
			mu.Lock()
			if b := beat[svc]; hb == 0 && b.After(t0.Add(50*time.Millisecond)) && hasHeartbeat(svc) {
				hb = b.Sub(t0)
			}
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
		}
		hbs := "-"
		if hb > 0 {
			hbs = fmt.Sprintf("%.1f", hb.Seconds())
		}
		fmt.Printf("%-20s %12.1f %18s\n", svc, ready.Seconds(), hbs)
		time.Sleep(4 * time.Second) // let it settle before the next kill
	}
}

func hasHeartbeat(svc string) bool {
	switch svc {
	case "bridge", "detector", "sprinkler-actuator", "door-actuator", "responder", "db-writer", "sensor-smoke", "sensor-co", "sensor-temperature":
		return true
	}
	return false
}
