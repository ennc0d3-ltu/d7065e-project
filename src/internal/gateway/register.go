package gateway

import (
	"fmt"
	"strings"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/buildsim"
)

// Default degradation per sensor kind (assumptions, see 04-physical-model §6).
// Lags are sim seconds, noise is in the sensor's own unit.
var defaults = []struct {
	kind   Kind
	suffix string
	sigma  float64
	lag    float64
}{
	{KindTemperature, "temp", 0.3, 30},
	{KindSmoke, "smoke", 0.005, 5},
	{KindCO, "co", 3, 10},
}

// RoomSensors returns the standard temperature, smoke and CO sensors for a
// room ("<level>/<room>"). Sensor ids carry the level because BuildSim ids
// are global and room names repeat between floors. Seeds are derived from
// baseSeed so runs are reproducible and sensors do not share a noise stream.
func RoomSensors(room string, baseSeed int64) []SensorSpec {
	prefix := strings.ReplaceAll(room, "/", "-")
	specs := make([]SensorSpec, 0, len(defaults))
	for i, d := range defaults {
		specs = append(specs, SensorSpec{
			ID: prefix + "-" + d.suffix, Room: room, Kind: d.kind,
			Sigma: d.sigma, Lag: d.lag, Seed: baseSeed*10 + int64(i),
		})
	}
	return specs
}

// FilterKinds keeps only the sensors of the named kinds ("smoke", "co",
// "temperature"), so one image can run as a smoke sensor process, a CO sensor
// process or a temperature sensor process. No names means every kind. A name
// that is not a kind is an error: a typo must not start a process that
// silently has nothing to do. Ids and noise seeds are untouched, so a sensor
// behaves the same alone as it does among the others.
func FilterKinds(specs []SensorSpec, names []string) ([]SensorSpec, error) {
	if len(names) == 0 {
		return specs, nil
	}
	want := map[Kind]bool{}
	for _, n := range names {
		k := Kind(n)
		if _, ok := units[k]; !ok {
			return nil, fmt.Errorf("gateway: unknown sensor kind %q (use smoke, co or temperature)", n)
		}
		want[k] = true
	}
	var out []SensorSpec
	for _, s := range specs {
		if want[s.Kind] {
			out = append(out, s)
		}
	}
	return out, nil
}

var equipmentTypes = map[Kind]string{
	KindTemperature: "temperature_sensor",
	KindSmoke:       "smoke_detector",
	KindCO:          "co_sensor",
}

// Equipment builds the BuildSim records that register the sensors, one
// equipment record per sensor so each draws its own 3D icon.
func Equipment(specs []SensorSpec) []buildsim.Equipment {
	out := make([]buildsim.Equipment, 0, len(specs))
	for _, s := range specs {
		level, room, _ := strings.Cut(s.Room, "/")
		out = append(out, buildsim.Equipment{
			ID: "eq-" + s.ID, Name: strings.ToUpper(string(s.Kind[:1])) + string(s.Kind[1:]) + " " + room,
			Type: equipmentTypes[s.Kind], Category: "monitoring",
			Level: level, Room: room, Status: "running",
			Sensors: []buildsim.Sensor{{
				ID: s.ID, Name: string(s.Kind), Type: string(s.Kind),
				DataType: "text", Unit: units[s.Kind],
			}},
		})
	}
	return out
}
