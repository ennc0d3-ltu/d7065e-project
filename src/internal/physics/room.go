package physics

import "math"

// Room is the ground-truth state of one well-mixed room.
type Room struct {
	Volume float64 // [m³]
	T      float64 // air temperature [°C]
	K      float64 // smoke extinction coefficient [1/m]
	C      float64 // CO concentration [ppm]
}

// Params are the model constants (see docs/L1/04-physical-model.md §4).
type Params struct {
	CTh     float64 // thermal mass of air + contents [kJ/K]
	KLoss   float64 // envelope + leakage loss [kW/K]
	QOcc    float64 // body heat per person [kW]
	QSpr    float64 // sprinkler cooling coefficient [kW/K]
	TWet    float64 // sprinkler water temperature [°C]
	TOut    float64 // outdoor temperature [°C]
	LamVent float64 // ventilation decay [1/s]
	LamSet  float64 // smoke settling [1/s]
	Beta    float64 // open-door exchange flow [m³/s]
}

// Inputs are the per-tick sources acting on one room.
type Inputs struct {
	Heat      float64 // effective heat-release rate [kW]
	Smoke     float64 // smoke source [1/m · m³/s]
	CO        float64 // CO source [ppm · m³/s]
	People    int     // head-count
	Sprinkler float64 // sprinkler state in [0,1]
}

// StepRoom advances one room by dt seconds, without inter-room exchange.
func StepRoom(r Room, p Params, in Inputs, dt float64) Room {
	dT := (p.KLoss*(p.TOut-r.T) +
		p.QOcc*float64(in.People) +
		in.Heat -
		p.QSpr*in.Sprinkler*(r.T-p.TWet)) / p.CTh
	dK := in.Smoke/r.Volume - (p.LamVent+p.LamSet)*r.K
	dC := in.CO/r.Volume - p.LamVent*r.C
	r.T += dT * dt
	r.K = math.Max(0, r.K+dK*dt)
	r.C = math.Max(0, r.C+dC*dt)
	return r
}
