package physics

import "math"

// Door links two rooms. Openness is a_ij from the physical model:
// 1 open, ~0.05 closed-unlocked leakage, ~0.02 closed fire door.
type Door struct {
	A, B     int
	Openness float64
}

// maxStepFraction bounds the explicit-Euler exchange per sub-step so a
// small room cannot overshoot (h · rate ≤ 0.5).
const maxStepFraction = 0.5

// Step advances all rooms by dt seconds: each room's local balance plus
// mixing through the doors. in may be shorter than rooms (missing = zero).
// The inputs are not modified.
func Step(rooms []Room, doors []Door, p Params, in []Inputs, dt float64) []Room {
	n := substeps(rooms, doors, p, dt)
	h := dt / float64(n)
	cur := append([]Room(nil), rooms...)
	for range n {
		next := make([]Room, len(cur))
		for i, r := range cur {
			var inp Inputs
			if i < len(in) {
				inp = in[i]
			}
			next[i] = StepRoom(r, p, inp, h)
		}
		for _, d := range doors {
			a, b := cur[d.A], cur[d.B]
			k := h * p.Beta * d.Openness
			next[d.A].T += k / a.Volume * (b.T - a.T)
			next[d.B].T += k / b.Volume * (a.T - b.T)
			next[d.A].K += k / a.Volume * (b.K - a.K)
			next[d.B].K += k / b.Volume * (a.K - b.K)
			next[d.A].C += k / a.Volume * (b.C - a.C)
			next[d.B].C += k / b.Volume * (a.C - b.C)
		}
		for i := range next {
			next[i].K = math.Max(0, next[i].K)
			next[i].C = math.Max(0, next[i].C)
		}
		cur = next
	}
	return cur
}

// substeps picks how many sub-steps keep every door's exchange stable.
func substeps(rooms []Room, doors []Door, p Params, dt float64) int {
	maxRate := 0.0
	for _, d := range doors {
		rate := p.Beta * d.Openness * (1/rooms[d.A].Volume + 1/rooms[d.B].Volume)
		maxRate = math.Max(maxRate, rate)
	}
	return max(1, int(math.Ceil(dt*maxRate/maxStepFraction)))
}
