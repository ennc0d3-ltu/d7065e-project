// Package dataset generates labelled feature vectors offline: physics truth,
// sensor degradation and the detector's own feature pipeline, with fixed
// seeds. It is the data source for training and for the D9 evaluation, and it
// uses the same code the running system uses, so train and serve cannot drift.
package dataset

import (
	"math/rand"
	"slices"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/detector"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/physics"
)

// SamplePeriod is the sensor publish period in sim seconds.
const SamplePeriod = 5

const (
	roomName   = "level0/R"
	roomVolume = 150 // m³
)

// Kind is the type of run.
type Kind int

const (
	KindNormal  Kind = iota // quiet room, people coming and going
	KindCooking             // benign smoke source
	KindDust                // benign smoke-only source
	KindFire                // real fire
	KindDrift               // smoke and CO from a fire next door, no heat in this room
)

func (k Kind) String() string {
	return [...]string{"normal", "cooking", "dust", "fire", "drift"}[k]
}

// RunSpec describes one simulated run of one room.
type RunSpec struct {
	Name        string
	Kind        Kind
	Alpha       float64 // fire growth rate [kW/s²] (KindFire)
	T0          float64 // event start [s]
	Duration    float64 // benign event length [s]
	Secs        int     // total run length [s]
	Seed        int64
	SprinklerAt float64 // sprinkler on from this time; <= 0 means never

	// Offline lists signals ("smoke", "co", "temperature") whose sensor stops
	// reporting at OfflineFrom, to test the detector with a failed sensor.
	Offline     []string
	OfflineFrom float64
}

// Point is one detector input: the feature vector at a sample time, and the
// ground-truth label (a fire is burning), which the detector never sees.
type Point struct {
	T    float64
	Vec  []float64
	Fire bool
}

// Run is a generated run.
type Run struct {
	Name   string
	Kind   Kind
	T0     float64
	Points []Point
}

// Generate simulates one run. It is deterministic for a given spec.
func Generate(spec RunSpec) Run {
	var scen []physics.Scenario
	switch spec.Kind {
	case KindFire:
		scen = append(scen, physics.Scenario{Kind: physics.KindFire, T0: spec.T0, Alpha: spec.Alpha})
	case KindDrift:
		scen = append(scen, physics.Scenario{Kind: physics.KindDrift, T0: spec.T0, Alpha: spec.Alpha})
	case KindCooking:
		scen = append(scen, physics.Scenario{Kind: physics.KindCooking, T0: spec.T0, Duration: spec.Duration})
	case KindDust:
		scen = append(scen, physics.Scenario{Kind: physics.KindDust, T0: spec.T0, Duration: spec.Duration})
	}
	eng := physics.NewEngine(physics.Config{
		Params: physics.DefaultParams(), Yields: physics.DefaultYields(), Sprinkler: physics.DefaultSprinkler(),
		Rooms:     []physics.RoomSpec{{Name: roomName, Volume: roomVolume}},
		Scenarios: scen,
	})

	type chain struct {
		kind gateway.Kind
		deg  *gateway.Degrader
	}
	var chains []chain
	for _, sp := range gateway.RoomSensors(roomName, spec.Seed) {
		chains = append(chains, chain{sp.Kind, gateway.NewDegrader(sp.Sigma, sp.Lag, sp.Seed)})
	}
	rf := detector.NewRoomFeatures(60, 3*SamplePeriod)
	rng := rand.New(rand.NewSource(spec.Seed))

	run := Run{Name: spec.Name, Kind: spec.Kind, T0: spec.T0}
	people := 0
	for sec := 0; sec < spec.Secs; sec++ {
		if sec%60 == 0 { // occupancy drifts: body heat is a source of normal variation
			people = max(0, min(6, people+rng.Intn(3)-1))
			eng.SetPeople(roomName, people)
		}
		if spec.SprinklerAt > 0 && float64(sec) >= spec.SprinklerAt {
			eng.SetSprinkler(roomName, 1)
		}
		eng.Tick(1)
		if (sec+1)%SamplePeriod != 0 {
			continue
		}
		now := float64(sec + 1)
		truth, _ := eng.Truth(roomName)
		for _, c := range chains {
			var v float64
			switch c.kind {
			case gateway.KindTemperature:
				v = truth.T
			case gateway.KindSmoke:
				v = truth.K
			case gateway.KindCO:
				v = truth.C
			}
			if now >= spec.OfflineFrom && slices.Contains(spec.Offline, string(c.kind)) {
				continue // the sensor is dead: nothing is published
			}
			if got, ok := c.deg.Observe(now, v); ok {
				if c.kind != gateway.KindTemperature {
					got = max(0, got)
				}
				rf.Add(gateway.Reading{Type: string(c.kind), Value: got, SimTime: now})
			}
		}
		vec, _ := rf.VectorDegraded(now)
		run.Points = append(run.Points, Point{
			T: now, Vec: vec, Fire: spec.Kind == KindFire && now >= spec.T0,
		})
	}
	return run
}
