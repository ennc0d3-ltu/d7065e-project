package detector

import (
	"math"
	"testing"
)

func TestTheDefaultThresholdsAreWithinTheHardLimits(t *testing.T) {
	if err := ValidateThresholds(DefaultThresholds()); err != nil {
		t.Fatalf("the shipped thresholds break their own limits: %v", err)
	}
}

func TestTighterThresholdsAreAccepted(t *testing.T) {
	th := DefaultThresholds()
	th.SmokeWarn, th.SmokeFire = 0.03, 0.10
	th.COWarn, th.COFire = 20, 60
	if err := ValidateThresholds(th); err != nil {
		t.Fatalf("a more sensitive setting was rejected: %v", err)
	}
}

func TestAThresholdLooserThanTheHardLimitIsRejected(t *testing.T) {
	// the limits are what keep a bad tuning run from blinding the system
	cases := map[string]func(*Thresholds){
		"smoke fire above the limit": func(th *Thresholds) { th.SmokeFire = MaxSmokeFire + 0.01 },
		"CO fire above the limit":    func(th *Thresholds) { th.COFire = MaxCOFire + 1 },
		"CO warn above the limit":    func(th *Thresholds) { th.COWarn = MaxCOWarn + 1 },
		"temperature fire above":     func(th *Thresholds) { th.TempFire = MaxTempFire + 1 },
		"rise fire above":            func(th *Thresholds) { th.RiseFire = MaxRiseFire + 0.01 },
		"smoke warn above":           func(th *Thresholds) { th.SmokeWarn = MaxSmokeWarn + 0.01 },
	}
	for name, mutate := range cases {
		th := DefaultThresholds()
		mutate(&th)
		if ValidateThresholds(th) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNonsenseThresholdsAreRejected(t *testing.T) {
	cases := map[string]func(*Thresholds){
		"warn above fire (smoke)":     func(th *Thresholds) { th.SmokeWarn = th.SmokeFire + 0.01 },
		"warn above fire (CO)":        func(th *Thresholds) { th.COWarn = th.COFire + 1 },
		"zero":                        func(th *Thresholds) { th.COFire = 0 },
		"negative":                    func(th *Thresholds) { th.SmokeWarn = -1 },
		"NaN":                         func(th *Thresholds) { th.TempWarn = math.NaN() },
		"infinite":                    func(th *Thresholds) { th.RiseFire = math.Inf(1) },
		"temperature warn above fire": func(th *Thresholds) { th.TempWarn = th.TempFire + 1 },
		"too sensitive to be useful":  func(th *Thresholds) { th.SmokeWarn = MinSmokeWarn / 2 },
	}
	for name, mutate := range cases {
		th := DefaultThresholds()
		mutate(&th)
		if ValidateThresholds(th) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEveryProblemIsReportedNotJustTheFirst(t *testing.T) {
	th := DefaultThresholds()
	th.COFire, th.SmokeFire = MaxCOFire+10, MaxSmokeFire+1
	err := ValidateThresholds(th)
	if err == nil {
		t.Fatal("accepted")
	}
	msg := err.Error()
	if !contains(msg, "CO") || !contains(msg, "smoke") {
		t.Fatalf("error %q should name both problems", msg)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
