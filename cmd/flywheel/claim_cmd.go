package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("claim", "claim a task so another lead knows it is driven", runClaim)
	registerHelp("claim", "flywheel claim <task> [--session S] [--ttl D] [--note TEXT] [--force] [--dir DIR]", func() *flag.FlagSet { fs, _ := claimFlags(); return fs })
	register("release", "release a claimed task", runRelease)
	registerHelp("release", "flywheel release <task> [--session S] [--force] [--dir DIR]", func() *flag.FlagSet { fs, _ := releaseFlags(); return fs })
	register("claims", "list every claim on the floor", runClaimsList)
	registerHelp("claims", "flywheel claims [--json] [--dir DIR]", func() *flag.FlagSet { fs, _ := claimsFlags(); return fs })
}

// claimOptions holds the parsed claim flags.
type claimOptions struct {
	dir     string
	session string
	ttl     time.Duration
	note    string
	force   bool
}

// claimFlags defines claim's flags once, so help and run share them.
func claimFlags() (*flag.FlagSet, *claimOptions) {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &claimOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.session, "session", "", "the claiming session")
	fs.DurationVar(&o.ttl, "ttl", 30*time.Minute, "how long the claim stays live")
	fs.StringVar(&o.note, "note", "", "free-form note")
	fs.BoolVar(&o.force, "force", false, "take over a live claim held by another session")
	return fs, o
}

// claimUsage prints the flywheel claim usage line.
func claimUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel claim <task> [--session S] [--ttl D] [--note TEXT] [--force] [--dir DIR]")
}

// runClaim implements `flywheel claim <task>`. A live claim held by a
// different session is refused with exit 6 unless --force takes it over.
func runClaim(args []string) {
	fs, o := claimFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel claim: %v\n", err)
		claimUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel claim: exactly one task id is required\n")
		claimUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]
	c, tookOver, err := flywheel.ClaimTask(o.dir, task, o.session, o.note, o.ttl, o.force, time.Now())
	if err != nil {
		var held *flywheel.ErrClaimHeld
		if errors.As(err, &held) {
			fmt.Fprintf(os.Stderr, "claim: %v; wait, or use --force\n", held)
			os.Exit(6)
		}
		fmt.Fprintf(os.Stderr, "flywheel claim: %v\n", err)
		os.Exit(1)
	}
	if tookOver != "" {
		fmt.Printf("claim: took over %s from %s\n", task, tookOver)
		return
	}
	fmt.Printf("claimed %s by %s until %s\n", task, c.Session, c.ExpiresAt)
}

// releaseOptions holds the parsed release flags.
type releaseOptions struct {
	dir     string
	session string
	force   bool
}

// releaseFlags defines release's flags once, so help and run share them.
func releaseFlags() (*flag.FlagSet, *releaseOptions) {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &releaseOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.session, "session", "", "the releasing session")
	fs.BoolVar(&o.force, "force", false, "release a live claim held by another session")
	return fs, o
}

// releaseUsage prints the flywheel release usage line.
func releaseUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel release <task> [--session S] [--force] [--dir DIR]")
}

// runRelease implements `flywheel release <task>`. A live claim held by a
// different session is refused with exit 6 unless --force overrides it.
// Releasing a task with no claim is a no-op (exit 0).
func runRelease(args []string) {
	fs, o := releaseFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel release: %v\n", err)
		releaseUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel release: exactly one task id is required\n")
		releaseUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]
	found, err := flywheel.ReleaseTask(o.dir, task, o.session, o.force, time.Now())
	if err != nil {
		var held *flywheel.ErrClaimHeld
		if errors.As(err, &held) {
			fmt.Fprintf(os.Stderr, "release: %v; wait, or use --force\n", held)
			os.Exit(6)
		}
		fmt.Fprintf(os.Stderr, "flywheel release: %v\n", err)
		os.Exit(1)
	}
	if !found {
		fmt.Printf("release: %s is not claimed\n", task)
		return
	}
	fmt.Printf("released %s\n", task)
}

// claimsOptions holds the parsed claims flags.
type claimsOptions struct {
	dir    string
	asJSON bool
}

// claimsFlags defines claims's flags once, so help and run share them.
func claimsFlags() (*flag.FlagSet, *claimsOptions) {
	fs := flag.NewFlagSet("claims", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &claimsOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.asJSON, "json", false, "print machine-readable JSON")
	return fs, o
}

// claimsUsage prints the flywheel claims usage line.
func claimsUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel claims [--json] [--dir DIR]")
}

// claimView augments a Claim with its derived liveness, for `flywheel claims
// --json`.
type claimView struct {
	flywheel.Claim
	Live bool `json:"live"`
}

// runClaimsList implements `flywheel claims`: every claim sorted by task. A
// claim file that failed to parse is skipped rather than fatal, so the
// listing always succeeds; exit 0 either way.
func runClaimsList(args []string) {
	fs, o := claimsFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel claims: %v\n", err)
		claimsUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel claims: unexpected argument %q\n", pos[0])
		claimsUsage(os.Stderr)
		os.Exit(2)
	}
	claims, _ := flywheel.ReadClaims(o.dir) // a malformed file is skipped, never fatal
	now := time.Now()
	if o.asJSON {
		views := make([]claimView, len(claims))
		for i, c := range claims {
			views[i] = claimView{Claim: c, Live: flywheel.ClaimLive(c, now)}
		}
		b, err := json.MarshalIndent(views, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel claims: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}
	if len(claims) == 0 {
		fmt.Println("no claims")
		return
	}
	for _, c := range claims {
		status := "expired"
		if flywheel.ClaimLive(c, now) {
			status = "live"
		}
		age := "?"
		if claimed, perr := time.Parse(time.RFC3339, c.ClaimedAt); perr == nil {
			age = flywheel.HumanAge(int(now.Sub(claimed).Seconds()))
		}
		fmt.Printf("%s  %s  %s  %s  %s\n", c.Task, c.Session, c.Note, age, status)
	}
}
