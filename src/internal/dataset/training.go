package dataset

// TrainingRuns is the one training set, used by both the trainer command and
// the evaluation, so the model that is evaluated is the model that is
// deployed. It holds normal and benign runs only: the one-class approach of
// proposal §4 never sees a fire during training, and a test enforces it.
// Seeds 1..112 are reserved for training; evaluation uses other seeds.
func TrainingRuns() []Run {
	var runs []Run
	for seed := int64(1); seed <= 12; seed++ {
		runs = append(runs, Generate(RunSpec{Name: "train-normal", Kind: KindNormal, Secs: 1800, Seed: seed}))
	}
	for seed := int64(101); seed <= 112; seed++ {
		runs = append(runs,
			Generate(RunSpec{Name: "train-cooking", Kind: KindCooking, T0: 200, Duration: 600, Secs: 1800, Seed: seed}),
			Generate(RunSpec{Name: "train-dust", Kind: KindDust, T0: 200, Duration: 600, Secs: 1800, Seed: seed + 50}))
	}
	return runs
}

// Vectors flattens runs into the feature vectors a model trains on.
func Vectors(runs []Run) [][]float64 {
	var out [][]float64
	for _, r := range runs {
		for _, p := range r.Points {
			out = append(out, p.Vec)
		}
	}
	return out
}
