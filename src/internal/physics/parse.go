package physics

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseBenign reads a benign scenario spec "<kind>:<level/room>:<start_s>:<duration_s>"
// where kind is cooking or dust. These are the false-alarm sources the
// detector must not discharge a sprinkler for. index maps a room name to its
// position in the engine's room list.
func ParseBenign(spec string, index map[string]int) (Scenario, error) {
	f := strings.Split(spec, ":")
	if len(f) != 4 {
		return Scenario{}, fmt.Errorf("benign %q: want kind:level/room:start_s:duration_s", spec)
	}
	var kind Kind
	switch f[0] {
	case "cooking":
		kind = KindCooking
	case "dust":
		kind = KindDust
	default:
		return Scenario{}, fmt.Errorf("benign %q: kind must be cooking or dust", spec)
	}
	room, ok := index[f[1]]
	if !ok {
		return Scenario{}, fmt.Errorf("benign %q: unknown room %q", spec, f[1])
	}
	start, err := strconv.ParseFloat(f[2], 64)
	if err != nil || start < 0 {
		return Scenario{}, fmt.Errorf("benign %q: start must be a non-negative number", spec)
	}
	dur, err := strconv.ParseFloat(f[3], 64)
	if err != nil || dur <= 0 {
		return Scenario{}, fmt.Errorf("benign %q: duration must be a positive number", spec)
	}
	return Scenario{Kind: kind, Room: room, T0: start, Duration: dur}, nil
}
