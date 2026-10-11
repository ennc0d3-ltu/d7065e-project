package detector

import (
	"math"
	"testing"
)

// vec builds a feature vector in FeatureNames order.
func vec(smoke, co, temp, dSmoke, dCO, dTemp float64) []float64 {
	return []float64{smoke, co, temp, dSmoke, dCO, dTemp}
}

func TestNormalAirIsTierNone(t *testing.T) {
	if got := DefaultThresholds().Classify(vec(0.0, 3, 21, 0, 0, 0)); got != TierNone {
		t.Fatalf("tier = %v, want none", got)
	}
}

func TestDustAloneIsOnlyAWarning(t *testing.T) {
	// lots of smoke, nothing else: a false-alarm source, never a fire
	got := DefaultThresholds().Classify(vec(0.4, 3, 21, 0.001, 0, 0))
	if got != TierWarning {
		t.Fatalf("tier = %v, want warning", got)
	}
}

func TestCookingStaysBelowFire(t *testing.T) {
	got := DefaultThresholds().Classify(vec(0.14, 7, 22.5, 0.0005, 0.01, 0.003))
	if got == TierFire {
		t.Fatal("cooking classified as fire")
	}
}

func TestRealFireIsTierFire(t *testing.T) {
	got := DefaultThresholds().Classify(vec(0.5, 300, 60, 0.02, 4, 0.3))
	if got != TierFire {
		t.Fatalf("tier = %v, want fire", got)
	}
}

func TestOneSignalAtFireLevelIsOnlyAWarning(t *testing.T) {
	got := DefaultThresholds().Classify(vec(0.0, 150, 21, 0, 0, 0)) // CO alone
	if got != TierWarning {
		t.Fatalf("tier = %v, want warning", got)
	}
}

func TestSmokeAndCOWithoutHeatIsArmedNotDischarged(t *testing.T) {
	// what a room sees when a fire next door fills it with smoke: two signals
	// at fire level, but no heat. Pre-action behaviour: armed (warning), and
	// the sprinkler is not discharged until heat shows up.
	got := DefaultThresholds().Classify(vec(0.3, 150, 21, 0, 0, 0))
	if got != TierArmed {
		t.Fatalf("tier = %v, want armed (no discharge without heat)", got)
	}
}

func TestSmokeAndCOWithHeatIsFire(t *testing.T) {
	got := DefaultThresholds().Classify(vec(0.3, 150, 36, 0, 0, 0)) // 36 °C: above the warning level
	if got != TierFire {
		t.Fatalf("tier = %v, want fire", got)
	}
}

func TestSmokeAndCOWithARisingTemperatureIsFire(t *testing.T) {
	got := DefaultThresholds().Classify(vec(0.3, 150, 22, 0, 0, 0.06)) // still cool, but climbing
	if got != TierFire {
		t.Fatalf("tier = %v, want fire", got)
	}
}

func TestSmokeAndCOWithTheTemperatureSensorDownStillFires(t *testing.T) {
	// no way to check for heat: safety first, so the gate steps aside (D2)
	got := DefaultThresholds().Classify(vec(0.3, 150, nan, 0, 0, nan))
	if got != TierFire {
		t.Fatalf("tier = %v, want fire: a dead heat sensor must not hide a fire", got)
	}
}

func TestHasHeatEvidence(t *testing.T) {
	th := DefaultThresholds()
	cases := []struct {
		name          string
		v             []float64
		evidence, ava bool
	}{
		{"cool and flat", vec(0, 3, 21, 0, 0, 0), false, true},
		{"warm", vec(0, 3, 36, 0, 0, 0), true, true},
		{"rising", vec(0, 3, 22, 0, 0, 0.06), true, true},
		{"sensor down", vec(0, 3, nan, 0, 0, nan), false, false},
	}
	for _, c := range cases {
		ev, ava := th.HasHeat(c.v)
		if ev != c.evidence || ava != c.ava {
			t.Errorf("%s: evidence=%v available=%v, want %v %v", c.name, ev, ava, c.evidence, c.ava)
		}
	}
}

func TestRequireHeatOnlyDowngradesAFireWithoutHeat(t *testing.T) {
	th := DefaultThresholds()
	cold := vec(0.3, 150, 21, 0, 0, 0)
	hot := vec(0.3, 150, 60, 0, 0, 0.3)
	if got := th.RequireHeat(TierFire, cold); got != TierArmed {
		t.Errorf("fire without heat -> %v, want armed", got)
	}
	if got := th.RequireHeat(TierFire, hot); got != TierFire {
		t.Errorf("fire with heat -> %v, want fire", got)
	}
	if got := th.RequireHeat(TierFire, vec(0.3, 150, nan, 0, 0, nan)); got != TierFire {
		t.Errorf("fire with the heat sensor down -> %v, want fire", got)
	}
	for _, tier := range []Tier{TierNone, TierWarning, TierArmed} {
		if got := th.RequireHeat(tier, cold); got != tier {
			t.Errorf("tier %v changed to %v", tier, got)
		}
	}
	if got := th.RequireHeat(TierFire, []float64{1}); got != TierWarning {
		t.Errorf("malformed vector -> %v, want warning", got)
	}
}

func TestTwoSignalsAtFireLevelIsFire(t *testing.T) {
	// two fire-level signals that include heat: smoke and temperature
	got := DefaultThresholds().Classify(vec(0.3, 3, 60, 0, 0, 0.3))
	if got != TierFire {
		t.Fatalf("tier = %v, want fire", got)
	}
}

func TestRapidTemperatureRiseCountsAsATemperatureSignal(t *testing.T) {
	// still cool in absolute terms, but climbing fast, with smoke as well
	got := DefaultThresholds().Classify(vec(0.3, 3, 25, 0, 0, 0.2))
	if got != TierFire {
		t.Fatalf("tier = %v, want fire", got)
	}
}

var nan = math.NaN()

func TestFireIsStillDetectedWhenOneSignalIsMissing(t *testing.T) {
	// smoke sensor down: CO and temperature both at fire level
	got := DefaultThresholds().Classify(vec(nan, 300, 60, nan, 4, 0.3))
	if got != TierFire {
		t.Fatalf("tier = %v, want fire: one failed sensor must not hide a fire", got)
	}
}

func TestOneMissingSignalNeverReadsAsNormal(t *testing.T) {
	got := DefaultThresholds().Classify(vec(nan, 3, 21, nan, 0, 0))
	if got != TierWarning {
		t.Fatalf("tier = %v, want warning: a stale signal is at least a warning", got)
	}
}

func TestWithOneSignalLeftItAloneCanConfirmAFire(t *testing.T) {
	// Safety over availability (D2): with CO and temperature down, smoke at
	// fire level cannot be told from dust, and is treated as a fire.
	got := DefaultThresholds().Classify(vec(0.5, nan, nan, 0.02, nan, nan))
	if got != TierFire {
		t.Fatalf("tier = %v, want fire", got)
	}
}

func TestWithOneSignalLeftANormalValueIsStillOnlyAWarning(t *testing.T) {
	got := DefaultThresholds().Classify(vec(0.0, nan, nan, 0, nan, nan))
	if got != TierWarning {
		t.Fatalf("tier = %v, want warning (two signals are stale)", got)
	}
}

func TestEverySignalMissingIsAWarning(t *testing.T) {
	got := DefaultThresholds().Classify(vec(nan, nan, nan, nan, nan, nan))
	if got != TierWarning {
		t.Fatalf("tier = %v, want warning", got)
	}
}

func TestDustStillCannotTriggerFireWhileOtherSignalsWork(t *testing.T) {
	// heat sensor down, smoke very high, CO normal: two signals remain and
	// only one is at fire level
	got := DefaultThresholds().Classify(vec(0.5, 3, nan, 0.001, 0, nan))
	if got == TierFire {
		t.Fatal("dust with a working CO sensor classified as fire")
	}
}

func TestWrongSizedVectorIsNotNormal(t *testing.T) {
	if got := DefaultThresholds().Classify([]float64{1, 2}); got != TierWarning {
		t.Fatalf("tier = %v: malformed input must never read as normal", got)
	}
}

func TestTierOrderingAndNames(t *testing.T) {
	if !(TierNone < TierWarning && TierWarning < TierArmed && TierArmed < TierFire) {
		t.Fatal("tiers must be ordered none < warning < armed < fire")
	}
	if TierNone.String() != "none" || TierWarning.String() != "warning" || TierArmed.String() != "armed" || TierFire.String() != "fire" {
		t.Fatal("tier names changed: they appear in the wire format")
	}
}
