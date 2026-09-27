//go:build race

package flywheel

import "time"

// raceEnabled reports whether the tests run under the race detector: CI's
// ubuntu job runs `go test -race` (.github/workflows/ci.yml), which slows
// CPU- and allocation-bound code by roughly ten times.
const raceEnabled = true

// budget is a wall-clock budget d as the build affords it: ten times d under
// the race detector, so a hang guard stays a hang guard there.
func budget(d time.Duration) time.Duration { return d * 10 }
