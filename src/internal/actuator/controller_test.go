package actuator

import (
	"strings"
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

const room = "level0/A109"

func newCtl() *Controller { return New(room, DefaultConfig()) }

func on(simTime float64, seq uint64) proto.Command {
	return proto.Command{Room: room, Action: proto.ActionOn, Reason: "test", Seq: seq, TTL: 30, SimTime: simTime}
}

func off(simTime float64, seq uint64) proto.Command {
	return proto.Command{Room: room, Action: proto.ActionOff, Reason: "all-clear", Seq: seq, TTL: 30, SimTime: simTime}
}

func beat(c *Controller, simTime float64) {
	c.OnHeartbeat(proto.Heartbeat{Process: "detector", Seq: uint64(simTime) + 1, SimTime: simTime})
}

func readings(c *Controller, now, smoke, co, temp float64) {
	for _, r := range []gateway.Reading{
		{Room: room, Type: "smoke", Value: smoke, SimTime: now},
		{Room: room, Type: "co", Value: co, SimTime: now},
		{Room: room, Type: "temperature", Value: temp, SimTime: now},
	} {
		c.OnReading(r)
	}
}

func fireAt(c *Controller, now float64) { readings(c, now, 0.5, 300, 60) }
func calmAt(c *Controller, now float64) { readings(c, now, 0, 3, 21) }
func dustAt(c *Controller, now float64) { readings(c, now, 0.5, 3, 21) }

func TestStartsOff(t *testing.T) {
	if out := newCtl().Step(0); out.On {
		t.Fatalf("sprinkler on at start: %+v", out)
	}
}

func TestValidOnCommandTurnsTheSprinklerOnAndLatches(t *testing.T) {
	c := newCtl()
	if !c.OnCommand(on(10, 1)) {
		t.Fatal("valid command rejected")
	}
	out := c.Step(10)
	if !out.On || out.Source != SourceDetector {
		t.Fatalf("output = %+v", out)
	}
	// the detector goes quiet but stays alive: still on (latched)
	beat(c, 20)
	if out := c.Step(20); !out.On {
		t.Fatalf("latch dropped: %+v", out)
	}
}

func TestExpiredCommandIsIgnored(t *testing.T) {
	c := newCtl()
	beat(c, 100) // sim time is now 100
	c.Step(100)
	if c.OnCommand(on(0, 1)) { // issued at t=0 with a 30 s TTL
		t.Fatal("an expired command was accepted")
	}
	if c.Step(100).On {
		t.Fatal("expired command turned the sprinkler on")
	}
}

func TestOlderCommandCannotOverrideANewerOne(t *testing.T) {
	c := newCtl()
	c.OnCommand(on(10, 2))
	if c.OnCommand(off(5, 1)) {
		t.Fatal("an older command was accepted")
	}
	if !c.Step(10).On {
		t.Fatal("older off command switched the sprinkler off")
	}
}

func TestDuplicateCommandIsHarmless(t *testing.T) {
	c := newCtl()
	c.OnCommand(on(10, 2))
	c.OnCommand(on(10, 2))
	if !c.Step(10).On {
		t.Fatal("duplicate broke the state")
	}
}

func TestADetectorRestartWithAResetSequenceIsStillObeyed(t *testing.T) {
	c := newCtl()
	c.OnCommand(on(10, 40))
	// detector restarted: seq back at 1, sim time moved on
	if !c.OnCommand(off(500, 1)) {
		t.Fatal("command from a restarted detector rejected")
	}
	if c.Step(500).On {
		t.Fatal("off command ignored")
	}
}

func TestOffCommandTurnsItOff(t *testing.T) {
	c := newCtl()
	c.OnCommand(on(10, 1))
	c.OnCommand(off(20, 2))
	if c.Step(20).On {
		t.Fatal("still on after an off command")
	}
}

func TestCommandForAnotherRoomIsIgnored(t *testing.T) {
	c := newCtl()
	cmd := on(10, 1)
	cmd.Room = "level0/A108"
	if c.OnCommand(cmd) {
		t.Fatal("accepted a command for another room")
	}
}

func TestInvalidCommandIsIgnored(t *testing.T) {
	c := newCtl()
	bad := on(10, 0) // no sequence number
	if c.OnCommand(bad) || c.Step(10).On {
		t.Fatal("invalid command had an effect")
	}
}

func TestSilentDetectorWithAFireTriggersTheLocalFallback(t *testing.T) {
	c := newCtl()
	beat(c, 0)
	var out Output
	for now := 0.0; now <= 120; now += 5 {
		fireAt(c, now) // the detector stays silent after t=0
		out = c.Step(now)
		if out.On {
			break
		}
	}
	if !out.On || out.Source != SourceFallback || !strings.Contains(out.Reason, "fallback") {
		t.Fatalf("output = %+v, want a local fallback activation", out)
	}
}

func TestFallbackWaitsForTheHeartbeatToLapse(t *testing.T) {
	c := newCtl()
	beat(c, 0)
	for now := 0.0; now <= 25; now += 5 { // detector last heard at 0, TTL 30
		fireAt(c, now)
		if out := c.Step(now); out.On {
			t.Fatalf("fallback fired at t=%v while the detector was still within its TTL", now)
		}
	}
}

func TestHealthyDetectorIsNotSecondGuessed(t *testing.T) {
	c := newCtl()
	for now := 0.0; now <= 300; now += 5 {
		beat(c, now) // alive, sending no command: it decided there is no fire
		fireAt(c, now)
		if out := c.Step(now); out.On {
			t.Fatalf("fallback overrode a live detector at t=%v", now)
		}
	}
}

func TestFallbackNeedsTwoSignalsLikeTheDetectorRules(t *testing.T) {
	c := newCtl()
	beat(c, 0)
	for now := 0.0; now <= 600; now += 5 {
		dustAt(c, now) // smoke only
		if out := c.Step(now); out.On {
			t.Fatalf("dust triggered the fallback at t=%v", now)
		}
	}
}

func TestLatchedSprinklerReleasesLocallyWhenTheDetectorDiedAndAirIsClear(t *testing.T) {
	c := newCtl()
	c.OnCommand(on(10, 1))
	beat(c, 10) // then the detector dies
	var releasedAt float64 = -1
	for now := 15.0; now <= 700; now += 5 {
		calmAt(c, now)
		if out := c.Step(now); !out.On {
			releasedAt = now
			break
		}
	}
	if releasedAt < 0 {
		t.Fatal("a latched sprinkler stayed on forever with a dead detector and clear air")
	}
	if releasedAt < 15+300 {
		t.Fatalf("released at t=%v, before ClearHold elapsed", releasedAt)
	}
}

func TestLatchHoldsWhileADetectorIsAlive(t *testing.T) {
	c := newCtl()
	c.OnCommand(on(10, 1))
	for now := 15.0; now <= 700; now += 5 {
		beat(c, now)
		calmAt(c, now)
		if out := c.Step(now); !out.On {
			t.Fatalf("released at t=%v without an off command from a live detector", now)
		}
	}
}

func TestFallbackOnReleasesAfterAClearHoldEvenIfTheDetectorReturns(t *testing.T) {
	c := newCtl()
	beat(c, 0)
	now := 0.0
	for ; now <= 200; now += 5 {
		fireAt(c, now)
		if c.Step(now).On {
			break
		}
	}
	if !c.Step(now).On {
		t.Fatal("setup: the fallback never fired")
	}
	// the detector comes back, idle, and the air clears
	var releasedAt float64 = -1
	for t0 := now + 5; t0 <= now+700; t0 += 5 {
		beat(c, t0)
		calmAt(c, t0)
		if !c.Step(t0).On {
			releasedAt = t0
			break
		}
	}
	if releasedAt < 0 {
		t.Fatal("fallback activation was never released")
	}
}

func TestRestartDuringAFireDoesNotSwitchTheSprinklerOff(t *testing.T) {
	c := newCtl()
	c.Restore(true) // BuildSim says the sprinkler is on
	if out := c.Step(0); !out.On {
		t.Fatalf("restored controller dropped an active sprinkler: %+v", out)
	}
	for now := 5.0; now <= 100; now += 5 { // detector alive and quiet, air still clear
		beat(c, now)
		calmAt(c, now)
		if out := c.Step(now); !out.On {
			t.Fatalf("restored sprinkler switched off at t=%v without an all-clear", now)
		}
	}
}

func TestRestoredSprinklerObeysTheDetectorsOffCommand(t *testing.T) {
	c := newCtl()
	c.Restore(true)
	c.OnCommand(off(50, 7))
	if c.Step(50).On {
		t.Fatal("restored sprinkler ignored an off command")
	}
}

func TestRestoredSprinklerReleasesAfterALocalAllClear(t *testing.T) {
	c := newCtl()
	c.Restore(true)
	var releasedAt float64 = -1
	for now := 0.0; now <= 700; now += 5 {
		beat(c, now)
		calmAt(c, now)
		if !c.Step(now).On {
			releasedAt = now
			break
		}
	}
	if releasedAt < 300 {
		t.Fatalf("released at t=%v, want after the 300 s all-clear hold", releasedAt)
	}
}

func TestRestoringOffChangesNothing(t *testing.T) {
	c := newCtl()
	c.Restore(false)
	if c.Step(0).On {
		t.Fatal("restoring off turned the sprinkler on")
	}
}

func TestNeverHavingHeardTheDetectorCountsAsSilenceAfterTheTTL(t *testing.T) {
	c := newCtl()
	var out Output
	for now := 0.0; now <= 200; now += 5 {
		fireAt(c, now)
		out = c.Step(now)
		if out.On {
			if now < 30 {
				t.Fatalf("fallback at t=%v, before the heartbeat TTL", now)
			}
			return
		}
	}
	t.Fatalf("no fallback although the detector was never heard: %+v", out)
}

func TestFreshCommandsKeepTheDetectorCountedAsAlive(t *testing.T) {
	c := newCtl()
	for now := 0.0; now <= 300; now += 5 {
		c.OnCommand(on(now, uint64(now)+1)) // refreshed every tick
		fireAt(c, now)
		out := c.Step(now)
		if out.On && out.Source != SourceDetector {
			t.Fatalf("t=%v: output %+v, the detector is alive", now, out)
		}
	}
}
