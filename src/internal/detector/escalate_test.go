package detector

import "testing"

func newEsc(mode Mode) *Escalator {
	cfg := DefaultConfig()
	cfg.Mode = mode
	return NewEscalator(cfg)
}

// run feeds `tier` once per second from t0 to t1 inclusive and returns the
// first decision whose action is a, with the time it happened (or -1).
func run(e *Escalator, t0, t1 float64, tier Tier, a Action) (Decision, float64) {
	for t := t0; t <= t1; t++ {
		if d := e.Update(t, tier); d.Action == a {
			return d, t
		}
	}
	return Decision{}, -1
}

func TestQuietRoomStaysIdle(t *testing.T) {
	e := newEsc(ModeBoth)
	for tm := 0.0; tm < 100; tm++ {
		if d := e.Update(tm, TierNone); d.Action != ActionNone || d.State != StateIdle {
			t.Fatalf("t=%v: %+v", tm, d)
		}
	}
}

func TestFirstWarningRaisesAPreAlertOnce(t *testing.T) {
	e := newEsc(ModeBoth)
	d := e.Update(0, TierWarning)
	if d.Action != ActionWarn || d.State != StateWarning {
		t.Fatalf("first warning: %+v", d)
	}
	if d := e.Update(1, TierWarning); d.Action != ActionNone {
		t.Fatalf("a continuing warning must not repeat the pre-alert: %+v", d)
	}
}

func TestFireTierActuatesOnlyAfterItHasPersisted(t *testing.T) {
	e := newEsc(ModeBoth)
	d, at := run(e, 0, 60, TierFire, ActionActuate)
	if at != 15 { // DefaultConfig FirePersist = 15 s
		t.Fatalf("actuated at t=%v, want 15; decision %+v", at, d)
	}
	if d.State != StateFire || d.Reason == "" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestAShortFireTierBlipDoesNotActuate(t *testing.T) {
	e := newEsc(ModeBoth)
	if _, at := run(e, 0, 10, TierFire, ActionActuate); at != -1 {
		t.Fatal("actuated during a 10 s blip")
	}
	run(e, 11, 200, TierNone, ActionActuate)
	if d := e.Update(201, TierFire); d.Action == ActionActuate {
		t.Fatal("the persistence clock must restart after the blip")
	}
}

func TestActuationHappensExactlyOnce(t *testing.T) {
	e := newEsc(ModeBoth)
	count := 0
	for tm := 0.0; tm < 200; tm++ {
		if e.Update(tm, TierFire).Action == ActionActuate {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("actuate decisions = %d, want exactly 1", count)
	}
}

func TestAPersistentArmedRoomEscalatesAfterTheArmedWindow(t *testing.T) {
	// smoke and CO at fire level with no heat: a safety valve, in case the heat
	// sensor lags or the fire is smouldering
	e := newEsc(ModeBoth)
	_, at := run(e, 0, 300, TierArmed, ActionActuate)
	if at != 120 { // ArmedPersist
		t.Fatalf("escalated at t=%v, want 120", at)
	}
}

func TestAPlainWarningNeverEscalatesOnItsOwn(t *testing.T) {
	// cooking, dust and steam give a persistent smoke-only warning: they must
	// never discharge a sprinkler (found by the system test T3)
	e := newEsc(ModeBoth)
	if _, at := run(e, 0, 5000, TierWarning, ActionActuate); at != -1 {
		t.Fatalf("a smoke-only warning discharged the sprinkler at t=%v", at)
	}
}

func TestAWarningWindowCanBeEnabledExplicitly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WarnPersist = 60
	e := NewEscalator(cfg)
	_, at := run(e, 0, 300, TierWarning, ActionActuate)
	if at != 60 {
		t.Fatalf("escalated at t=%v, want 60 once enabled", at)
	}
}

func TestArmedThenPlainWarningRestartsTheArmedClock(t *testing.T) {
	e := newEsc(ModeBoth)
	run(e, 0, 100, TierArmed, ActionActuate) // 100 s armed
	run(e, 101, 150, TierWarning, ActionActuate)
	// armed again: the 120 s must count from here, not from t=0
	if _, at := run(e, 151, 250, TierArmed, ActionActuate); at != -1 && at < 151+120 {
		t.Fatalf("escalated at t=%v: the armed clock did not restart", at)
	}
}

func TestAnArmedRoomRaisesAPreAlertOnce(t *testing.T) {
	e := newEsc(ModeBoth)
	if d := e.Update(0, TierArmed); d.Action != ActionWarn || d.State != StateWarning {
		t.Fatalf("first armed tick: %+v", d)
	}
	if d := e.Update(1, TierArmed); d.Action != ActionNone {
		t.Fatalf("the pre-alert repeated: %+v", d)
	}
}

func TestSprinklerLatchesUntilAllClearHeldLongEnough(t *testing.T) {
	e := newEsc(ModeBoth)
	_, at := run(e, 0, 60, TierFire, ActionActuate)
	// the score drops to none, but the latch holds
	for tm := at + 1; tm < at+300; tm++ {
		if d := e.Update(tm, TierNone); d.State != StateFire || d.Action == ActionRelease {
			t.Fatalf("released early at t=%v: %+v", tm, d)
		}
	}
	d := e.Update(at+300, TierNone) // ClearHold = 300 s
	if d.Action != ActionRelease || d.State != StateIdle {
		t.Fatalf("expected an all-clear release: %+v", d)
	}
}

func TestAnyRenewedSignalRestartsTheAllClearTimer(t *testing.T) {
	e := newEsc(ModeBoth)
	_, at := run(e, 0, 60, TierFire, ActionActuate)
	for tm := at + 1; tm < at+200; tm++ {
		e.Update(tm, TierNone)
	}
	e.Update(at+200, TierWarning) // a flicker of smoke
	for tm := at + 201; tm < at+200+300; tm++ {
		if d := e.Update(tm, TierNone); d.Action == ActionRelease {
			t.Fatalf("released at t=%v although the clear timer should have restarted", tm)
		}
	}
	if d := e.Update(at+200+300, TierNone); d.Action != ActionRelease {
		t.Fatalf("expected release 300 s after the flicker: %+v", d)
	}
}

func TestOperatorConfirmActuatesAtOnceFromAWarning(t *testing.T) {
	e := newEsc(ModeBoth)
	e.Update(0, TierWarning)
	d := e.Confirm(5)
	if d.Action != ActionActuate || d.State != StateFire {
		t.Fatalf("confirm: %+v", d)
	}
}

func TestOperatorConfirmWithNoWarningIsIgnored(t *testing.T) {
	e := newEsc(ModeBoth)
	if d := e.Confirm(5); d.Action != ActionNone || d.State != StateIdle {
		t.Fatalf("confirm on a quiet room: %+v", d)
	}
}

func TestOperatorCancelSuppressesActuationForTheHoldPeriod(t *testing.T) {
	e := newEsc(ModeBoth)
	e.Update(0, TierWarning)
	if d := e.Cancel(5); d.State != StateCancelled || d.Action != ActionNone {
		t.Fatalf("cancel: %+v", d)
	}
	// the fire tier persists for far longer than FirePersist, still suppressed
	if _, at := run(e, 6, 300, TierFire, ActionActuate); at != -1 && at < 305 {
		t.Fatalf("actuated at t=%v inside the cancel hold", at)
	}
}

func TestCancelExpiresSoAWrongCancelCannotHideARealFire(t *testing.T) {
	e := newEsc(ModeBoth)
	e.Update(0, TierWarning)
	e.Cancel(5) // CancelHold = 300 s, so it lapses at t=305
	_, at := run(e, 6, 1000, TierFire, ActionActuate)
	if at == -1 {
		t.Fatal("a persisting fire was never actuated after the cancel expired (H3)")
	}
	if at < 305 {
		t.Fatalf("actuated at t=%v, before the cancel expired", at)
	}
}

func TestALatchedFireCannotBeCancelledOnlyReleased(t *testing.T) {
	e := newEsc(ModeBoth)
	run(e, 0, 60, TierFire, ActionActuate)
	if d := e.Cancel(70); d.State != StateFire {
		t.Fatalf("cancel changed a latched fire: %+v", d)
	}
	d := e.Release(80)
	if d.Action != ActionRelease || d.State != StateIdle {
		t.Fatalf("release: %+v", d)
	}
}

func TestOperatorModeWaitsForConfirmOrTheTimeout(t *testing.T) {
	e := newEsc(ModeOperator)
	// persistence is satisfied at t=15, but actuation waits for the operator
	if _, at := run(e, 0, 40, TierFire, ActionActuate); at != -1 {
		t.Fatalf("operator mode actuated at t=%v without confirmation", at)
	}
	// OperatorTimeout = 60 s after the criterion was met (t=15): falls back at 75
	_, at := run(e, 41, 200, TierFire, ActionActuate)
	if at != 75 {
		t.Fatalf("persistence fallback at t=%v, want 75", at)
	}
}

func TestOperatorModeConfirmBeatsTheTimeout(t *testing.T) {
	e := newEsc(ModeOperator)
	run(e, 0, 20, TierFire, ActionActuate)
	if d := e.Confirm(21); d.Action != ActionActuate {
		t.Fatalf("confirm: %+v", d)
	}
}

func TestPersistenceModeIgnoresTheOperator(t *testing.T) {
	e := newEsc(ModePersistence)
	e.Update(0, TierWarning)
	if d := e.Confirm(1); d.Action != ActionNone {
		t.Fatalf("persistence mode obeyed a confirm: %+v", d)
	}
	if d := e.Cancel(2); d.State == StateCancelled {
		t.Fatalf("persistence mode obeyed a cancel: %+v", d)
	}
}

func TestEveryOperatorActionAndTransitionIsAudited(t *testing.T) {
	e := newEsc(ModeBoth)
	e.Update(0, TierWarning)
	e.Cancel(5)
	e.Confirm(6) // ignored while cancelled, but still on the record
	log := e.Audit()
	if len(log) < 3 {
		t.Fatalf("audit has %d entries: %+v", len(log), log)
	}
	for _, a := range log {
		if a.Event == "" {
			t.Fatalf("audit entry without an event: %+v", a)
		}
	}
	if log[len(log)-1].Time != 6 {
		t.Fatalf("last audit entry is not the confirm at t=6: %+v", log)
	}
}
