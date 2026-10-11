package actuator

import (
	"strings"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
)

// SprinklerID is the BuildSim actuator id for a room's sprinkler. It carries
// the level because BuildSim ids are global and room names repeat between
// floors. physics-sim reads the same id back, so the two must agree.
func SprinklerID(room string) string {
	return strings.ReplaceAll(room, "/", "-") + "-sprinkler"
}

// Equipment is the BuildSim record that registers a room's sprinkler, starting off.
func Equipment(room string) buildsim.Equipment {
	level, name, _ := strings.Cut(room, "/")
	return buildsim.Equipment{
		ID: "eq-" + SprinklerID(room), Name: "Sprinkler " + name,
		Type: "sprinkler", Category: "fire",
		Level: level, Room: name, Status: "running",
		Actuators: []buildsim.Actuator{{
			ID: SprinklerID(room), Name: "Sprinkler", Type: "sprinkler", State: StateString(false),
		}},
	}
}

// StateString is the actuator state BuildSim stores and physics-sim reads.
func StateString(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
