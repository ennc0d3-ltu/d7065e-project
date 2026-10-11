package eval

import (
	"testing"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/dataset"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/physics"
)

// stub classifier driven by the first feature (smoke): >=2 fire, >=1 warning.
func stub(v []float64) detector.Tier {
	switch {
	case v[0] >= 2:
		return detector.TierFire
	case v[0] >= 1:
		return detector.TierWarning
	}
	return detector.TierNone
}

func run(kind dataset.Kind, t0 float64, smoke ...float64) dataset.Run {
	r := dataset.Run{Name: kind.String(), Kind: kind, T0: t0}
	for i, s := range smoke {
		now := float64((i + 1) * dataset.SamplePeriod)
		r.Points = append(r.Points, dataset.Point{
			T: now, Vec: []float64{s, 0, 0, 0, 0, 0}, Fire: kind == dataset.KindFire && now >= t0,
		})
	}
	return r
}

func TestFireIsDetectedAfterThePersistenceWindowWithADelay(t *testing.T) {
	// ignition at t=10 (sample 2); fire tier from sample 3 on (t=15,20,25)
	r := run(dataset.KindFire, 10, 0, 0, 3, 3, 3, 3)
	res := EvaluateRun(r, stub, 3)
	if !res.Detected || res.FalseAlarm {
		t.Fatalf("result = %+v", res)
	}
	// persisted at the third consecutive fire sample (t=25): delay 15 s
	if res.DelaySec != 15 {
		t.Fatalf("delay = %v, want 15", res.DelaySec)
	}
}

func TestAFireThatIsNeverDetectedIsAMiss(t *testing.T) {
	res := EvaluateRun(run(dataset.KindFire, 10, 0, 0, 1, 1, 1, 1), stub, 3)
	if res.Detected {
		t.Fatalf("a warning-level run was counted as detected: %+v", res)
	}
}

func TestAnAlarmBeforeIgnitionIsAFalseAlarm(t *testing.T) {
	res := EvaluateRun(run(dataset.KindFire, 100, 3, 3, 3, 3, 3), stub, 3)
	if !res.FalseAlarm || res.Detected {
		t.Fatalf("result = %+v", res)
	}
}

func TestBenignRunWithAFireTierIsAFalseAlarm(t *testing.T) {
	res := EvaluateRun(run(dataset.KindDust, 0, 0, 3, 3, 3, 0), stub, 3)
	if !res.FalseAlarm {
		t.Fatalf("result = %+v", res)
	}
}

func TestShortSpikeBelowThePersistenceWindowDoesNotAlarm(t *testing.T) {
	res := EvaluateRun(run(dataset.KindCooking, 0, 0, 3, 3, 0, 0), stub, 3)
	if res.FalseAlarm {
		t.Fatalf("a two-sample spike alarmed with persistence 3: %+v", res)
	}
}

func TestWarningsAloneAreNotAlarms(t *testing.T) {
	res := EvaluateRun(run(dataset.KindDust, 0, 1, 1, 1, 1, 1, 1), stub, 3)
	if res.FalseAlarm {
		t.Fatalf("warning tier counted as a fire alarm: %+v", res)
	}
}

func TestAWarningThatPersistsMarksTheRunAsWarned(t *testing.T) {
	res := EvaluateRun(run(dataset.KindDrift, 0, 0, 1, 1, 1, 1, 0), stub, 3)
	if !res.Warned || res.FalseAlarm {
		t.Fatalf("result = %+v: persistent warnings are armed, not an alarm", res)
	}
}

func TestAShortWarningBlipDoesNotMarkTheRunAsWarned(t *testing.T) {
	res := EvaluateRun(run(dataset.KindDrift, 0, 0, 1, 1, 0, 0), stub, 3)
	if res.Warned {
		t.Fatalf("result = %+v", res)
	}
}

func TestWarningLeadTimeIsMeasuredFromIgnition(t *testing.T) {
	// ignition at t=10; warnings from t=15, held for three samples by t=25
	r := run(dataset.KindFire, 10, 0, 0, 1, 1, 1, 3, 3, 3)
	res := EvaluateRun(r, stub, 3)
	if !res.Warned || res.WarnDelaySec != 15 {
		t.Fatalf("result = %+v, want a warning 15 s after ignition", res)
	}
}

func TestAWarningBeforeIgnitionIsNotCreditedAsEarlyWarning(t *testing.T) {
	res := EvaluateRun(run(dataset.KindFire, 100, 1, 1, 1, 1, 1), stub, 3)
	if !res.WarnedEarly || res.WarnDelaySec != 0 {
		t.Fatalf("result = %+v: a warning with no fire burning is a nuisance, not a lead time", res)
	}
}

func TestSummaryReportsTheMedianWarningLeadTimeAndNuisanceWarnings(t *testing.T) {
	results := []RunResult{
		{Kind: dataset.KindFire, Detected: true, DelaySec: 100, Warned: true, WarnDelaySec: 20},
		{Kind: dataset.KindFire, Detected: true, DelaySec: 110, Warned: true, WarnDelaySec: 40},
		{Kind: dataset.KindFire, Detected: true, DelaySec: 120, Warned: true, WarnDelaySec: 30},
		{Kind: dataset.KindFire, Detected: true, DelaySec: 90, Warned: true, WarnedEarly: true},
		{Kind: dataset.KindNormal, Warned: true},
		{Kind: dataset.KindNormal},
		{Kind: dataset.KindNormal},
		{Kind: dataset.KindNormal},
	}
	s := Summarize(results)
	if s.MedianWarnDelaySec != 30 {
		t.Fatalf("median warning lead = %v, want 30 (the early warning is excluded)", s.MedianWarnDelaySec)
	}
	if s.NuisanceWarnRate != 0.25 {
		t.Fatalf("nuisance rate = %v, want 0.25 (1 of 4 quiet rooms warned)", s.NuisanceWarnRate)
	}
}

func TestAFireAlarmCountsAsWarnedToo(t *testing.T) {
	res := EvaluateRun(run(dataset.KindFire, 0, 3, 3, 3, 3), stub, 3)
	if !res.Warned {
		t.Fatalf("result = %+v: a fire-tier run has a tier at or above warning", res)
	}
}

func TestSummaryReportsHowManyDriftRunsWereArmed(t *testing.T) {
	results := []RunResult{
		{Kind: dataset.KindDrift, Warned: true},
		{Kind: dataset.KindDrift, Warned: false},
		{Kind: dataset.KindDrift, Warned: true},
		{Kind: dataset.KindDrift, Warned: true},
		{Kind: dataset.KindDust},
	}
	s := Summarize(results)
	if s.Drift != 4 || s.ArmedRate != 0.75 {
		t.Fatalf("summary = %+v, want 4 drift runs and 0.75 armed", s)
	}
}

func TestSummaryRatesAndMedianDelay(t *testing.T) {
	results := []RunResult{
		{Kind: dataset.KindFire, Detected: true, DelaySec: 10},
		{Kind: dataset.KindFire, Detected: true, DelaySec: 30},
		{Kind: dataset.KindFire, Detected: true, DelaySec: 20},
		{Kind: dataset.KindFire, Detected: false},
		{Kind: dataset.KindCooking, FalseAlarm: true},
		{Kind: dataset.KindDust},
		{Kind: dataset.KindNormal},
		{Kind: dataset.KindNormal},
	}
	s := Summarize(results)
	if s.Fires != 4 || s.Benign != 4 {
		t.Fatalf("counts = %+v", s)
	}
	if s.Recall != 0.75 || s.FalseAlarmRate != 0.25 || s.MedianDelaySec != 20 {
		t.Fatalf("summary = %+v", s)
	}
}

// --- the actual evaluation, on simulator data (D9) ---

// heldOutRuns use seeds the training never saw.
func heldOutRuns() []dataset.Run {
	var runs []dataset.Run
	for seed := int64(1001); seed <= 1010; seed++ {
		for _, a := range []float64{physics.AlphaSlow, physics.AlphaMedium, physics.AlphaFast} {
			runs = append(runs, dataset.Generate(dataset.RunSpec{Kind: dataset.KindFire, Alpha: a, T0: 300, Secs: 1200, Seed: seed}))
		}
		runs = append(runs,
			dataset.Generate(dataset.RunSpec{Kind: dataset.KindCooking, T0: 200, Duration: 600, Secs: 1500, Seed: seed + 500}),
			dataset.Generate(dataset.RunSpec{Kind: dataset.KindDust, T0: 200, Duration: 600, Secs: 1500, Seed: seed + 600}),
			dataset.Generate(dataset.RunSpec{Kind: dataset.KindNormal, Secs: 1500, Seed: seed + 700}),
			dataset.Generate(dataset.RunSpec{Kind: dataset.KindDrift, Alpha: physics.AlphaMedium, T0: 200, Secs: 1200, Seed: seed + 800}))
	}
	return runs
}

// heldOutWithOffline is the held-out set with one sensor dead for the whole run.
func heldOutWithOffline(signal string) []dataset.Run {
	var runs []dataset.Run
	for seed := int64(2001); seed <= 2010; seed++ {
		off := func(s dataset.RunSpec) dataset.RunSpec { s.Offline = []string{signal}; return s }
		for _, a := range []float64{physics.AlphaSlow, physics.AlphaMedium, physics.AlphaFast} {
			runs = append(runs, dataset.Generate(off(dataset.RunSpec{Kind: dataset.KindFire, Alpha: a, T0: 300, Secs: 1200, Seed: seed})))
		}
		runs = append(runs,
			dataset.Generate(off(dataset.RunSpec{Kind: dataset.KindCooking, T0: 200, Duration: 600, Secs: 1500, Seed: seed + 500})),
			dataset.Generate(off(dataset.RunSpec{Kind: dataset.KindDust, T0: 200, Duration: 600, Secs: 1500, Seed: seed + 600})),
			dataset.Generate(off(dataset.RunSpec{Kind: dataset.KindNormal, Secs: 1500, Seed: seed + 700})))
	}
	return runs
}

// A fire plus one failed sensor must still be detected (D2, NFR6, NFR12).
func TestFiresAreDetectedWithAnySingleSensorOffline(t *testing.T) {
	var train [][]float64
	for _, r := range dataset.TrainingRuns() {
		for _, p := range r.Points {
			train = append(train, p.Vec)
		}
	}
	forest, err := detector.Train(train, 100, 256, 42)
	if err != nil {
		t.Fatal(err)
	}
	candidates := map[string]Classifier{
		"rules":  RulesClassifier(detector.DefaultThresholds()),
		"forest": ModelClassifier(detector.NewModel(forest, train)),
	}
	for _, signal := range []string{"smoke", "co", "temperature"} {
		held := heldOutWithOffline(signal)
		for name, c := range candidates {
			var results []RunResult
			for _, r := range held {
				results = append(results, EvaluateRun(r, c, 3))
			}
			s := Summarize(results)
			t.Logf("%-6s %-11s offline: recall=%.2f false-alarm=%.2f median-delay=%.0fs",
				name, signal, s.Recall, s.FalseAlarmRate, s.MedianDelaySec)
			if s.Recall < 0.9 {
				t.Errorf("%s with %s offline: recall %.2f below 0.9", name, signal, s.Recall)
			}
			if s.FalseAlarmRate > 0.1 {
				t.Errorf("%s with %s offline: false-alarm rate %.2f above 0.1", name, signal, s.FalseAlarmRate)
			}
		}
	}
}

func TestRulesModelAndDeployedOnHeldOutRuns(t *testing.T) {
	train := dataset.Vectors(dataset.TrainingRuns())
	forest, err := detector.Train(train, 100, 256, 42)
	if err != nil {
		t.Fatal(err)
	}
	model := detector.NewModel(forest, train)
	th := detector.DefaultThresholds()

	held := heldOutRuns()
	const persist = 3
	candidates := []struct {
		name string
		c    Classifier
		safe bool // must meet the targets and keep drifting-smoke rooms armed
	}{
		{"rules", RulesClassifier(th), true},
		{"model alone", ModelClassifier(model), false},
		{"deployed", Classifier(detector.Deployed(th, model)), true},
	}
	for _, cand := range candidates {
		var results []RunResult
		for _, r := range held {
			results = append(results, EvaluateRun(r, cand.c, persist))
		}
		s := Summarize(results)
		t.Logf("%-11s recall=%.2f false-alarm=%.2f alarm-delay=%3.0fs warning-lead=%3.0fs nuisance-warnings=%.2f drift-armed=%.2f (fires=%d benign=%d drift=%d)",
			cand.name, s.Recall, s.FalseAlarmRate, s.MedianDelaySec, s.MedianWarnDelaySec, s.NuisanceWarnRate, s.ArmedRate, s.Fires, s.Benign, s.Drift)
		if !cand.safe {
			continue
		}
		if s.Recall < 0.9 {
			t.Errorf("%s recall %.2f below the NFR2 target of 0.9", cand.name, s.Recall)
		}
		if s.FalseAlarmRate > 0.1 {
			t.Errorf("%s false-alarm rate %.2f above 0.1", cand.name, s.FalseAlarmRate)
		}
		if s.ArmedRate < 0.9 {
			t.Errorf("%s armed only %.2f of the smoke-drift rooms: a smoky room must at least warn", cand.name, s.ArmedRate)
		}
	}
}

// TestDelayByGrowthClass prints the median alarm delay per fire growth class
// and detector: the data behind the chart in the report (section 11).
func TestDelayByGrowthClass(t *testing.T) {
	train := dataset.Vectors(dataset.TrainingRuns())
	forest, err := detector.Train(train, 100, 256, 42)
	if err != nil {
		t.Fatal(err)
	}
	model := detector.NewModel(forest, train)
	th := detector.DefaultThresholds()
	classes := []struct {
		name  string
		alpha float64
	}{{"slow", physics.AlphaSlow}, {"medium", physics.AlphaMedium}, {"fast", physics.AlphaFast}}
	dets := []struct {
		name string
		c    Classifier
	}{{"rules", RulesClassifier(th)}, {"model", ModelClassifier(model)}, {"deployed", Classifier(detector.Deployed(th, model))}}
	for _, d := range dets {
		for _, cl := range classes {
			var results []RunResult
			for seed := int64(1001); seed <= 1010; seed++ {
				run := dataset.Generate(dataset.RunSpec{Kind: dataset.KindFire, Alpha: cl.alpha, T0: 300, Secs: 1200, Seed: seed})
				results = append(results, EvaluateRun(run, d.c, 3))
			}
			s := Summarize(results)
			if s.Recall < 0.9 {
				t.Errorf("%s/%s recall %.2f", d.name, cl.name, s.Recall)
			}
			t.Logf("DELAY %s %s %.0f", d.name, cl.name, s.MedianDelaySec)
		}
	}
}
