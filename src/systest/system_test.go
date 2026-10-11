//go:build system

package systest

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/mqttx"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

// All timings are wall seconds. The physics runs at 50 sim s per wall second,
// so a fire detected after about 110 sim s is on after about 2 s of wall time.

const (
	room  = "level0/A109"
	other = "level0/A108"
)

func fireOptions() options {
	return options{fire: room + ":medium:30"} // no door: the rooms are isolated
}

// T3: only a real fire discharges a sprinkler.
func TestT3_OnlyARealFireDischargesTheSprinkler(t *testing.T) {
	t.Run("fire", func(t *testing.T) {
		r := newRig(t, options{})
		opt := fireOptions()
		r.startAll(opt)
		waitFor(t, 30*time.Second, "the sprinkler in the burning room to come on", func() bool { return r.sprinkler(room) == "on" })
		t.Logf("sprinkler on at sim t=%.0f s (fire started at 30 s)", r.simTime())
		stays(t, 2*time.Second, "the neighbouring room's sprinkler staying off", func() bool { return r.sprinkler(other) == "off" })
	})
	for _, kind := range []string{"cooking", "dust"} {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t, options{})
			r.startAll(options{benign: kind + ":" + room + ":0:900"})
			// roughly 750 sim seconds of a benign source, near its worst
			stays(t, 15*time.Second, "no sprinkler discharging for "+kind, func() bool {
				return r.sprinkler(room) == "off" && r.sprinkler(other) == "off"
			})
		})
	}
}

// T6: the latch holds when the detector dies mid-fire (FR5, H2).
func TestT6_LatchedSprinklerSurvivesTheDetectorDying(t *testing.T) {
	r := newRig(t, options{})
	r.startAll(fireOptions())
	waitFor(t, 30*time.Second, "the sprinkler to come on", func() bool { return r.sprinkler(room) == "on" })
	r.procs["detector"].kill()
	stays(t, 4*time.Second, "the sprinkler staying on after the detector was killed", func() bool { return r.sprinkler(room) == "on" })
}

// T8: with the detector dead, the actuator's local rules still catch a fire (D6, NFR3).
func TestT8_ActuatorFallbackFiresWhenTheDetectorIsDown(t *testing.T) {
	r := newRig(t, options{})
	r.startAll(fireOptions(), "detector") // the detector never starts
	waitFor(t, 40*time.Second, "the actuator's local fallback to switch the sprinkler on", func() bool { return r.sprinkler(room) == "on" })
	if !strings.Contains(r.procs["sprinkler-actuator"].logs(), "local fallback") {
		t.Fatalf("the sprinkler came on, but not through the local fallback:\n%s", tail(r.procs["sprinkler-actuator"].logs(), 1500))
	}
	t.Logf("fallback activated at sim t=%.0f s", r.simTime())
}

// T10: every control-plane process comes back after a crash within 30 s (NFR4).
func TestT10_EachProcessRecoversAfterACrash(t *testing.T) {
	opt := options{}
	r := newRig(t, opt)
	r.startAll(opt)
	waitFor(t, 15*time.Second, "readings flowing into BuildSim", func() bool { return r.bridgeStats().Written > 50 })

	for _, name := range []string{"sensor-gateway", "bridge", "detector", "sprinkler-actuator", "responder", "dashboard"} {
		t.Run(name, func(t *testing.T) {
			r.procs[name].kill()
			began := time.Now()
			r.restart(name, opt)
			if took := time.Since(began); took > 30*time.Second {
				t.Fatalf("%s took %v to become ready, want at most 30 s", name, took)
			}
			t.Logf("%s ready again after %v", name, time.Since(began).Round(time.Millisecond))
		})
	}
	before := r.bridgeStats().Written
	waitFor(t, 15*time.Second, "readings still flowing after every process was restarted", func() bool { return r.bridgeStats().Written > before+50 })
}

// T10b: restarting the actuator in the middle of a fire must not switch the sprinkler off.
func TestT10b_ActuatorRestartMidFireKeepsTheSprinklerOn(t *testing.T) {
	r := newRig(t, options{})
	opt := fireOptions()
	r.startAll(opt)
	waitFor(t, 30*time.Second, "the sprinkler to come on", func() bool { return r.sprinkler(room) == "on" })
	r.procs["sprinkler-actuator"].kill()
	r.restart("sprinkler-actuator", opt)
	stays(t, 4*time.Second, "the sprinkler staying on across the actuator restart", func() bool { return r.sprinkler(room) == "on" })
}

// T12: BuildSim restarting loses all its state; the system must recover on its own (NFR4).
func TestT12_StackRecoversWhenBuildSimRestarts(t *testing.T) {
	r := newRig(t, options{})
	r.startAll(options{})
	waitFor(t, 15*time.Second, "readings flowing", func() bool { return r.bridgeStats().Written > 50 })

	r.restartBuildSim() // everything registered with it is gone
	for name, p := range r.procs {
		if !p.alive() {
			t.Fatalf("%s died when BuildSim restarted:\n%s", name, tail(p.logs(), 1500))
		}
	}
	waitFor(t, 20*time.Second, "the sprinkler actuators to be registered again", func() bool { return r.sprinkler(room) == "off" })
	waitFor(t, 20*time.Second, "the sensors to be registered again and readings accepted", func() bool {
		before := r.bridgeStats()
		time.Sleep(500 * time.Millisecond)
		after := r.bridgeStats()
		return after.Written > before.Written+20
	})
}

// T9: the broker dying must not leave a fire unattended (D6, NFR3).
func TestT9_SprinklerStillFiresWhenTheBrokerDies(t *testing.T) {
	r := newRig(t, options{})
	r.startAll(options{fire: room + ":medium:300"}) // the fire starts after the broker is gone
	waitFor(t, 15*time.Second, "readings flowing", func() bool { return r.bridgeStats().Written > 50 })
	if r.sprinkler(room) == "on" {
		t.Fatal("test setup: the sprinkler was already on before the broker died")
	}
	r.stopBroker()
	waitFor(t, 60*time.Second, "the sprinkler to come on without a broker", func() bool { return r.sprinkler(room) == "on" })
	// it must be the actuator's own rules on the direct path, not a late command
	if logs := r.procs["sprinkler-actuator"].logs(); !strings.Contains(logs, `"source":"fallback"`) {
		t.Fatalf("the sprinkler came on, but not through the local fallback:\n%s", tail(logs, 1500))
	}
	t.Logf("sprinkler on through the local fallback at sim t=%.0f s, broker down", r.simTime())
}

// T13: a fire with one failed sensor is still caught (D2, NFR5).
func TestT13_FireIsStillDetectedWithAFailedSensor(t *testing.T) {
	cases := []struct{ name, sensor, mode string }{
		{"smoke sensor stuck", "level0-A109-smoke", "stuck"},
		{"CO sensor offline", "level0-A109-co", "offline"},
		{"temperature sensor offline", "level0-A109-temp", "offline"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, options{})
			r.startAll(options{fire: room + ":medium:120"})
			code, err := httpPost(r.procs["sensor-gateway"].url("/faults/"+c.sensor), `{"mode":"`+c.mode+`"}`)
			if err != nil || code != 204 {
				t.Fatalf("fault injection: code=%d err=%v", code, err)
			}
			waitFor(t, 40*time.Second, "the sprinkler to come on despite the failed sensor", func() bool { return r.sprinkler(room) == "on" })
			t.Logf("sprinkler on at sim t=%.0f s with %s", r.simTime(), c.name)
		})
	}
}

// T9b: the broker dies and comes back. What the gateway could not send is
// replayed for history, and responders are told about a fire that started
// during the outage (D13).
func TestT9b_OutageIsReplayedForHistoryAndResponders(t *testing.T) {
	r := newRig(t, options{})
	r.startAll(options{fire: room + ":medium:400"})
	waitFor(t, 15*time.Second, "readings flowing", func() bool { return r.bridgeStats().Written > 50 })

	r.stopBroker()
	outageStart := r.simTime()
	time.Sleep(6 * time.Second) // about 300 sim s without a broker; the fire starts in here
	outageEnd := r.simTime()
	r.restartBroker()
	t.Logf("broker down from sim %.0f to %.0f s", outageStart, outageEnd)

	watcher, err := mqttx.Connect(context.Background(), r.mqttURL, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close(context.Background())
	var mu sync.Mutex
	var replayed []gateway.Reading
	var dispatches []proto.Dispatch
	_ = watcher.Subscribe(context.Background(), "replay/sensors/#", 1, func(_ string, p []byte) {
		var rd gateway.Reading
		if json.Unmarshal(p, &rd) == nil {
			mu.Lock()
			replayed = append(replayed, rd)
			mu.Unlock()
		}
	})
	_ = watcher.Subscribe(context.Background(), proto.DispatchTopic, 1, func(_ string, p []byte) {
		var d proto.Dispatch
		if json.Unmarshal(p, &d) == nil {
			mu.Lock()
			dispatches = append(dispatches, d)
			mu.Unlock()
		}
	})

	waitFor(t, 30*time.Second, "the readings from the outage to be replayed for history", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, rd := range replayed {
			if rd.SimTime > outageStart && rd.SimTime < outageEnd {
				return true
			}
		}
		return false
	})
	waitFor(t, 60*time.Second, "responders to be told about the fire", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, d := range dispatches {
			if d.Kind == "fire" || d.Kind == "update" {
				return true
			}
		}
		return false
	})
	mu.Lock()
	t.Logf("replayed %d readings from the outage; %d dispatch(es) after the broker returned", len(replayed), len(dispatches))
	mu.Unlock()
}
