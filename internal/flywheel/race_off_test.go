//go:build !race

package flywheel

import "time"

// raceEnabled reports whether the tests run under the race detector: CI's
// ubuntu job runs `go test -race` (.github/workflows/ci.yml), which slows
// CPU- and allocation-bound code by roughly ten times.
const raceEnabled = false

// budget is a wall-clock budget d as the build affords it: d itself without
// the race detector (race_on_test.go multiplies it).
func budget(d time.Duration) time.Duration { return d }
