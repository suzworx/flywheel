package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"flywheel/internal/flywheel"
)

func init() {
	register("doctor", "probe every configured model and classify its availability", runDoctor)
	registerHelp("doctor", "flywheel doctor [--dir DIR]", func() *flag.FlagSet { fs, _ := doctorFlags(); return fs })
}

// doctorOptions holds the parsed `flywheel doctor` flags.
type doctorOptions struct {
	dir string
}

// doctorUsage prints the flywheel doctor usage line.
func doctorUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel doctor [--dir DIR]")
}

// doctorFlags defines doctor's flags once, so help and run share them.
func doctorFlags() (*flag.FlagSet, *doctorOptions) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &doctorOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	return fs, o
}

// runDoctor implements `flywheel doctor`: probe the selected worker's model
// then its fallbacks, in config order, through the worker's own adapter, and
// print one "<model>: <class>" line per probe. Exit 0 when every probe is
// ok, 1 when any is not (or fails to run), 2 on a usage error.
func runDoctor(args []string) {
	fs, o := doctorFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel doctor: %v\n", err)
		doctorUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel doctor: no positional arguments\n")
		doctorUsage(os.Stderr)
		os.Exit(2)
	}
	probes, err := flywheel.Doctor(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel doctor: %v\n", err)
		os.Exit(1)
	}
	for _, p := range probes {
		fmt.Printf("%s: %s\n", p.Model, p.Class)
	}
	if !flywheel.DoctorAllOK(probes) {
		os.Exit(1)
	}
}
