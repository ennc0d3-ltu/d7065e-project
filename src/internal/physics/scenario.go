package physics

// Kind is the type of event driving a room's sources.
type Kind int

const (
	KindFire    Kind = iota // real fire: heat, smoke and CO grow together
	KindCooking             // benign: some smoke, little CO and heat
	KindDust                // benign: smoke only
	KindDrift               // smoke and CO from a fire elsewhere, no heat here
)

// Yields convert heat-release rate into smoke and CO source rates.
type Yields struct {
	GS  float64 // smoke per kW  [1/m · m³/s per kW]
	GCO float64 // CO per kW     [ppm · m³/s per kW]
}

// Benign-source constants (see docs/L1/04-physical-model.md §5).
const (
	cookingHeat        = 5.0   // kW
	cookingSmokeFactor = 0.5   // of the fire smoke yield at the same heat
	cookingCOFactor    = 0.05  // of the fire CO yield at the same heat
	dustSmoke          = 0.03  // 1/m · m³/s, constant
	defaultQMax        = 1000. // kW
)

// DefaultParams are the calibrated model constants (assumptions, to tune).
func DefaultParams() Params {
	return Params{
		CTh: 300, KLoss: 2, QOcc: 0.1, QSpr: 0.5, TWet: 15, TOut: 20,
		LamVent: 0.0008, LamSet: 0.0002, Beta: 1,
	}
}

// DefaultYields are calibrated so a medium fire in a ~150 m³ room passes
// K ≈ 0.1 1/m in about 60 s and CO ≈ 100 ppm in about 90 s.
func DefaultYields() Yields { return Yields{GS: 0.019, GCO: 5.5} }

// DefaultSprinkler is the suppression time constant (assumed).
func DefaultSprinkler() Sprinkler { return Sprinkler{TauSup: 60} }

// FireInputs turns an effective heat-release rate into room sources.
func FireInputs(q float64, y Yields) Inputs {
	return Inputs{Heat: q, Smoke: y.GS * q, CO: y.GCO * q}
}

// Scenario describes one event in one room. Times are sim seconds.
type Scenario struct {
	Kind     Kind
	Room     int
	T0       float64 // start
	Duration float64 // benign kinds only; a fire runs until suppressed
	Alpha    float64 // fire growth rate [kW/s²]
	QMax     float64 // fire limit [kW]; 0 means the default
}

// InputsAt returns the sources at time t. s is the sprinkler state and ta
// the time it came on (only fires are affected by the sprinkler).
func (sc Scenario) InputsAt(t, s, ta float64, sp Sprinkler, y Yields) Inputs {
	switch sc.Kind {
	case KindFire:
		qmax := sc.QMax
		if qmax == 0 {
			qmax = defaultQMax
		}
		f := Fire{Alpha: sc.Alpha, T0: sc.T0, QMax: qmax}
		return FireInputs(f.Effective(t, s, ta, sp), y)
	case KindDrift:
		// Smoke and CO that arrive from a fire in another room follow that
		// fire's growth, but bring no heat of their own: the heat is lost in
		// the doorway, the smoke is not. Modelled as a fire's smoke and CO
		// yields without its heat release.
		qmax := sc.QMax
		if qmax == 0 {
			qmax = defaultQMax
		}
		q := Fire{Alpha: sc.Alpha, T0: sc.T0, QMax: qmax}.HRR(t)
		in := FireInputs(q, y)
		in.Heat = 0
		return in
	case KindCooking:
		if !sc.active(t) {
			return Inputs{}
		}
		return Inputs{
			Heat:  cookingHeat,
			Smoke: y.GS * cookingHeat * cookingSmokeFactor,
			CO:    y.GCO * cookingHeat * cookingCOFactor,
		}
	case KindDust:
		if !sc.active(t) {
			return Inputs{}
		}
		return Inputs{Smoke: dustSmoke}
	}
	return Inputs{}
}

func (sc Scenario) active(t float64) bool {
	return t >= sc.T0 && t < sc.T0+sc.Duration
}
