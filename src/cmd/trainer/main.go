// trainer fits the anomaly model on the deterministic training set (normal and
// benign runs only, never a fire) and writes it to a file the detector loads.
//
//	go run ./cmd/trainer -out models/detector.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/dataset"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
)

func main() {
	out := flag.String("out", "models/detector.json", "where to write the model")
	trees := flag.Int("trees", 100, "number of isolation trees")
	psi := flag.Int("psi", 256, "sub-sample size per tree")
	seed := flag.Int64("seed", 42, "training seed (same seed, same model)")
	flag.Parse()

	train := dataset.Vectors(dataset.TrainingRuns())
	forest, err := detector.Train(train, *trees, *psi, *seed)
	if err != nil {
		fail(err)
	}
	model := detector.NewModel(forest, train)

	raw, err := json.Marshal(model)
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		fail(err)
	}
	th := model.Thresholds(0)
	fmt.Printf("trained on %d vectors (%d trees, sub-sample %d, seed %d)\n", len(train), *trees, *psi, *seed)
	fmt.Printf("thresholds, all sensors working: warn %.3f fire %.3f\n", th.Warn, th.Fire)
	fmt.Printf("wrote %s (%d bytes)\n", *out, len(raw))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "trainer:", err)
	os.Exit(1)
}
