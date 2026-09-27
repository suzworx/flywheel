package main

import "testing"

// TestShipRequeueFlag (issue #591): an explicit --requeue 0 parses to never
// (a negative ShipOptions.Requeue), the flag absent to 0, which Ship takes as
// the default 2, and --requeue 3 to 3. On the unfixed code there is no
// --requeue flag and the first parse fails.
func TestShipRequeueFlag(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"T", "--requeue", "0"}, -1},
		{[]string{"T"}, 0},
		{[]string{"--requeue", "3", "T"}, 3},
	} {
		fs, o := shipFlags()
		if _, err := parseArgs(fs, c.args); err != nil {
			t.Fatalf("parse %v: %v", c.args, err)
		}
		if got := shipRequeue(fs, o.requeue); got != c.want {
			t.Errorf("%v: Requeue = %d, want %d", c.args, got, c.want)
		}
	}
}
