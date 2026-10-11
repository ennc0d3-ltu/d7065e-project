package doors

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
)

// ParseDoors reads "level0/A1008:470,level0/A1024B:512": each room and the
// entry node of its door in BuildSim's walkable graph. A room may appear once.
func ParseDoors(spec string) ([]Door, error) {
	var out []Door
	seen := map[string]bool{}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		room, node, ok := strings.Cut(item, ":")
		if !ok {
			return nil, fmt.Errorf("doors: %q is not <level>/<room>:<entry node>", item)
		}
		if level, name, ok := strings.Cut(room, "/"); !ok || level == "" || name == "" {
			return nil, fmt.Errorf("doors: %q is not <level>/<room>", room)
		}
		id, err := strconv.Atoi(strings.TrimSpace(node))
		if err != nil || id < 0 {
			return nil, fmt.Errorf("doors: %q has no valid entry node", item)
		}
		if seen[room] {
			return nil, fmt.Errorf("doors: %s listed twice", room)
		}
		seen[room] = true
		out = append(out, Door{Room: room, EntryNode: id})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("doors: no doors given")
	}
	return out, nil
}

// BuildSimDoors turns the controller's decision into BuildSim's door objects.
// Door ids carry the level (room names repeat between floors), every door is
// closed (containment doors are not actuated, D7), and a door without a
// decision is unlocked: when in doubt, people must be able to leave.
func BuildSimDoors(doors []Door, desired map[string]Lock) []buildsim.Door {
	out := make([]buildsim.Door, 0, len(doors))
	for _, d := range doors {
		level, room, _ := strings.Cut(d.Room, "/")
		lock := desired[d.Room]
		if lock != Locked {
			lock = Unlocked
		}
		out = append(out, buildsim.Door{
			ID: "door-" + level + "-" + room, Name: "Door " + room, Kind: "door",
			Level: level, Room: room, EntryNodeID: d.EntryNode,
			State: "closed", LockState: string(lock),
		})
	}
	return out
}
