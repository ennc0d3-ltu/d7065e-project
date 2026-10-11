package doors

import (
	"testing"
	"time"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/proto"
)

const (
	roomA = "level0/A1008"
	roomB = "level0/A1024B"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newController(t *testing.T) (*Controller, *fakeClock) {
	t.Helper()
	clk := &fakeClock{now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	cfg := Config{
		Doors:    []Door{{Room: roomA, EntryNode: 470}, {Room: roomB, EntryNode: 512}},
		Silence:  3 * time.Second, // 30 sim s at 10x
		QuietFor: 20,              // sim seconds
	}
	return New(cfg, clk.Now), clk
}

func beat(c *Controller, sim float64) {
	c.OnHeartbeat(proto.Heartbeat{Process: "detector", Seq: 1, SimTime: sim})
}

func unlock(room string, sim, ttl float64) proto.DoorCommand {
	return proto.DoorCommand{Room: room, Action: proto.DoorUnlock, Seq: 1, TTL: ttl, SimTime: sim, Reason: "fire"}
}

// settle brings a controller to its normal state: detector heard and quiet.
func settle(c *Controller, clk *fakeClock) {
	beat(c, 100)
	beat(c, 125)
}

func want(t *testing.T, c *Controller, room string, lock Lock) {
	t.Helper()
	if got := c.Desired()[room]; got != lock {
		t.Fatalf("%s is %s, want %s", room, got, lock)
	}
}

func TestBeforeTheDetectorIsHeardEveryDoorIsUnlocked(t *testing.T) {
	// fail-safe on start-up: a restarted actuator cannot know there is no fire
	c, _ := newController(t)
	want(t, c, roomA, Unlocked)
	want(t, c, roomB, Unlocked)
}

func TestDoorsRelockOnlyAfterTheDetectorHasBeenQuietForAWhile(t *testing.T) {
	c, _ := newController(t)
	beat(c, 100)
	want(t, c, roomA, Unlocked) // heard, but not yet 20 sim s of quiet
	beat(c, 115)
	want(t, c, roomA, Unlocked)
	beat(c, 120)
	want(t, c, roomA, Locked)
	want(t, c, roomB, Locked)
}

func TestAnUnlockCommandUnlocksThatDoorUntilItsTTLRunsOut(t *testing.T) {
	c, clk := newController(t)
	settle(c, clk)
	if err := c.OnCommand(unlock(roomA, 130, 30)); err != nil {
		t.Fatal(err)
	}
	want(t, c, roomA, Unlocked)
	want(t, c, roomB, Locked) // only the room in alarm
	beat(c, 159)
	want(t, c, roomA, Unlocked)
	beat(c, 160) // 130 + 30
	want(t, c, roomA, Locked)
}

func TestRefreshingTheCommandKeepsTheDoorUnlocked(t *testing.T) {
	c, clk := newController(t)
	settle(c, clk)
	for sim := 130.0; sim <= 400; sim += 5 {
		beat(c, sim)
		if err := c.OnCommand(unlock(roomA, sim, 30)); err != nil {
			t.Fatal(err)
		}
		want(t, c, roomA, Unlocked)
	}
	beat(c, 440) // the commands stop: the alarm is over
	want(t, c, roomA, Locked)
}

func TestADeadDetectorUnlocksEveryDoor(t *testing.T) {
	c, clk := newController(t)
	settle(c, clk)
	want(t, c, roomA, Locked)
	clk.Advance(2 * time.Second)
	want(t, c, roomA, Locked) // not silent for long enough yet
	clk.Advance(2 * time.Second)
	want(t, c, roomA, Unlocked)
	want(t, c, roomB, Unlocked)
	beat(c, 130) // the detector is back
	want(t, c, roomA, Locked)
}

func TestAnExpiredCommandIsIgnored(t *testing.T) {
	c, clk := newController(t)
	settle(c, clk)
	beat(c, 200)
	_ = c.OnCommand(unlock(roomA, 100, 30)) // ended at 130
	want(t, c, roomA, Locked)
}

func TestABadCommandIsRejectedAndChangesNothing(t *testing.T) {
	c, clk := newController(t)
	settle(c, clk)
	bad := unlock(roomA, 130, 30)
	bad.Action = "open"
	if c.OnCommand(bad) == nil {
		t.Fatal("a command with the wrong action was accepted")
	}
	if c.OnCommand(unlock("level0/nowhere", 130, 30)) == nil {
		t.Fatal("a command for a room without a door was accepted")
	}
	want(t, c, roomA, Locked)
}

func TestShutdownUnlocksEveryDoorAndTheyStayUnlocked(t *testing.T) {
	c, clk := newController(t)
	settle(c, clk)
	c.ReleaseAll()
	want(t, c, roomA, Unlocked)
	beat(c, 500)
	want(t, c, roomA, Unlocked)
	want(t, c, roomB, Unlocked)
}

func TestAnotherProcessesHeartbeatDoesNotCountAsTheDetector(t *testing.T) {
	c, clk := newController(t)
	settle(c, clk)
	clk.Advance(5 * time.Second)
	c.OnHeartbeat(proto.Heartbeat{Process: "bridge", Seq: 1, SimTime: 999})
	want(t, c, roomA, Unlocked) // still silent: the detector did not speak
}

func TestASimulationClockResetStartsTheQuietPeriodAgain(t *testing.T) {
	// physics-sim restarted: sim time went back to 0, so old command deadlines
	// mean nothing; behave as at start-up (unlocked) until it is quiet again
	c, clk := newController(t)
	settle(c, clk)
	_ = c.OnCommand(unlock(roomA, 130, 30000)) // a deadline far in the old time frame
	beat(c, 3)                                 // the clock went back
	want(t, c, roomA, Unlocked)
	want(t, c, roomB, Unlocked)
	beat(c, 30)
	want(t, c, roomA, Locked) // the old deadline was forgotten
	want(t, c, roomB, Locked)
}

func TestDoorsListsTheDoorsWithTheirEntryNodes(t *testing.T) {
	c, _ := newController(t)
	got := c.Doors()
	if len(got) != 2 || got[0].Room != roomA || got[0].EntryNode != 470 {
		t.Fatalf("doors = %+v", got)
	}
}
