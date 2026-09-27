package main

import (
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestDoctorLine checks a probe with a Detail prints it in parentheses and
// one without prints "<model>: <class>" as before (issue #637).
func TestDoctorLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		p    flywheel.DoctorProbe
		want string
	}{
		{flywheel.DoctorProbe{Model: "m1", Class: flywheel.ClassOK}, "m1: ok"},
		{flywheel.DoctorProbe{Model: "m2", Class: flywheel.ClassError, Detail: "exit 1: no input"}, "m2: error (exit 1: no input)"},
	}
	for _, c := range cases {
		if got := doctorLine(c.p); got != c.want {
			t.Errorf("doctorLine(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}
