package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("doctor", "probe every configured model and classify its availability", runDoctor)
	registerHelp("doctor", "flywheel doctor [--worker NAME] [--record] [--dir DIR]", func() *flag.FlagSet { fs, _ := doctorFlags(); return fs })
}

// doctorOptions holds the parsed `flywheel doctor` flags.
type doctorOptions struct {
	dir    string
	worker string
	record bool
}

// doctorUsage prints the flywheel doctor usage line.
func doctorUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel doctor [--worker NAME] [--record] [--dir DIR]")
}

// doctorFlags defines doctor's flags once, so help and run share them.
func doctorFlags() (*flag.FlagSet, *doctorOptions) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &doctorOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.worker, "worker", "", "probe this worker's model and fallbacks (default: the default worker)")
	fs.BoolVar(&o.record, "record", false, "record each probe as a probed event; an ok probe closes the model's breaker")
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
	var probes []flywheel.DoctorProbe
	if o.worker != "" {
		var err error
		probes, err = flywheel.DoctorWorker(o.dir, o.worker)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel doctor: %v\n", err)
			if errors.Is(err, flywheel.ErrUnknownWorker) {
				os.Exit(2) // a usage error; a broken config stays exit 1
			}
			os.Exit(1)
		}
	} else {
		var err error
		probes, err = flywheel.Doctor(o.dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel doctor: %v\n", err)
			os.Exit(1)
		}
	}
	for _, p := range probes {
		fmt.Printf("%s: %s\n", p.Model, p.Class)
	}
	if o.record {
		if err := flywheel.RecordProbes(o.dir, probes); err != nil {
			fmt.Fprintf(os.Stderr, "flywheel doctor: record probes: %v\n", err)
			os.Exit(1)
		}
	}
	if !flywheel.DoctorAllOK(probes) {
		os.Exit(1)
	}
}
