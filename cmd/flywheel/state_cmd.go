package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("state", "derive and print flywheel state", runState)
	registerHelp("state", "flywheel state [flags]", func() *flag.FlagSet { fs, _ := stateFlags(); return fs })
}

// stateOptions holds the parsed state flags.
type stateOptions struct {
	dir    string
	asJSON bool
}

// stateFlags defines state's flags once, so help and run share them.
func stateFlags() (*flag.FlagSet, *stateOptions) {
	fs := flag.NewFlagSet("state", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &stateOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.asJSON, "json", false, "print the derived state as JSON")
	return fs, o
}

func countKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func runState(args []string) {
	fs, o := stateFlags()
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel state: %v\n", err)
		usage(os.Stderr)
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "flywheel state: unexpected argument %q\n", fs.Arg(0))
		usage(os.Stderr)
		os.Exit(2)
	}
	st, err := flywheel.WriteState(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel state: %v\n", err)
		os.Exit(1)
	}
	if o.asJSON {
		b, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel state: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}
	for _, ts := range st.Tasks {
		fmt.Printf("%s  %s  attempts=%d  %s\n", ts.ID, ts.Status, ts.Attempts, ts.Model)
	}
	ids := countKeys(st.Counts)
	slices.Sort(ids)
	line := ""
	for _, k := range ids {
		if line != "" {
			line = line + " "
		}
		line = line + fmt.Sprintf("%s=%d", k, st.Counts[k])
	}
	if line == "" {
		fmt.Println("counts")
	} else {
		fmt.Println("counts " + line)
	}
}
