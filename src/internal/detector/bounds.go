package detector

import (
	"errors"
	"fmt"
	"math"
)

// Hard limits on what tuning may do to the rules (decision D15). A threshold
// can be made more sensitive, never loose enough to hide a fire: whatever a
// tuner or a registry update proposes, these are checked in code, so a bad
// tuning run or a poisoned bundle can only make the system jumpier, not blind.
//
// The upper limits sit well below dangerous levels (CO of 200 ppm is already
// hazardous within hours), and the lower limits keep the rules usable.
const (
	MaxSmokeWarn = 0.15 // extinction coefficient [1/m]
	MaxSmokeFire = 0.30
	MaxCOWarn    = 50.0 // ppm
	MaxCOFire    = 200.0
	MaxTempWarn  = 50.0 // °C
	MaxTempFire  = 70.0
	MaxRiseWarn  = 0.15 // K/s
	MaxRiseFire  = 0.30

	MinSmokeWarn = 0.01
	MinCOWarn    = 5.0
	MinTempWarn  = 25.0
	MinRiseWarn  = 0.01
)

// ValidateThresholds rejects thresholds that are out of range, nonsensical, or
// looser than the hard limits. It reports every problem, not just the first.
func ValidateThresholds(th Thresholds) error {
	var errs []error
	check := func(name string, warn, fire, minWarn, maxWarn, maxFire float64) {
		for _, v := range []float64{warn, fire} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
				errs = append(errs, fmt.Errorf("%s thresholds must be finite and positive (warn %v, fire %v)", name, warn, fire))
				return
			}
		}
		if warn > fire {
			errs = append(errs, fmt.Errorf("%s warning level %v is above its fire level %v", name, warn, fire))
		}
		if warn < minWarn {
			errs = append(errs, fmt.Errorf("%s warning level %v is below the usable minimum %v", name, warn, minWarn))
		}
		if warn > maxWarn {
			errs = append(errs, fmt.Errorf("%s warning level %v is above the hard limit %v", name, warn, maxWarn))
		}
		if fire > maxFire {
			errs = append(errs, fmt.Errorf("%s fire level %v is above the hard limit %v", name, fire, maxFire))
		}
	}
	check("smoke", th.SmokeWarn, th.SmokeFire, MinSmokeWarn, MaxSmokeWarn, MaxSmokeFire)
	check("CO", th.COWarn, th.COFire, MinCOWarn, MaxCOWarn, MaxCOFire)
	check("temperature", th.TempWarn, th.TempFire, MinTempWarn, MaxTempWarn, MaxTempFire)
	check("rate-of-rise", th.RiseWarn, th.RiseFire, MinRiseWarn, MaxRiseWarn, MaxRiseFire)
	return errors.Join(errs...)
}
