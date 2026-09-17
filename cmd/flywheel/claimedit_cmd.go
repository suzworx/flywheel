package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"flywheel/internal/flywheel"
)

func init() {
	register("claim-edit", "declare a lead's own mid-wave edit so the owns check attributes it", runClaimEdit)
	registerHelp("claim-edit", "flywheel claim-edit --paths P1,P2 --session S [--note TEXT] [--dir DIR]", func() *flag.FlagSet { fs, _ := claimEditFlags(); return fs })
}

// claimEditOptions holds the parsed claim-edit flags.
type claimEditOptions struct {
	dir     string
	paths   string
	session string
	note    string
}

// claimEditFlags defines claim-edit's flags once, so help and run share them.
func claimEditFlags() (*flag.FlagSet, *claimEditOptions) {
	fs := flag.NewFlagSet("claim-edit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &claimEditOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.paths, "paths", "", "comma-separated repo-relative paths the lead edited")
	fs.StringVar(&o.session, "session", "", "the lead session that made the edits")
	fs.StringVar(&o.note, "note", "", "free-form note")
	return fs, o
}

// claimEditUsage prints the flywheel claim-edit usage line.
func claimEditUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel claim-edit --paths P1,P2 --session S [--note TEXT] [--dir DIR]")
}

// runClaimEdit implements `flywheel claim-edit`: it reads each claimed path,
// hashes its content, and appends one lead_edit event declaring that the
// lead's own session edited those exact contents after dispatch, so the owns
// check attributes the paths instead of refusing every unit dispatched before
// the edit. A path that does not exist at claim time records a deletion
// marker, so "I deleted this file" is expressible too. A claim pattern that
// is not a literal path is refused: a wildcard cannot be bound to one content
// hash, and ownsContains would let it exempt the whole tree (issue #258).
func runClaimEdit(args []string) {
	fs, o := claimEditFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: %v\n", err)
		claimEditUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: unexpected argument %q\n", pos[0])
		claimEditUsage(os.Stderr)
		os.Exit(2)
	}
	if o.session == "" {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: --session is required\n")
		claimEditUsage(os.Stderr)
		os.Exit(2)
	}
	var owns []string
	for _, p := range strings.Split(o.paths, ",") {
		if p = strings.TrimSpace(p); p != "" {
			owns = append(owns, p)
		}
	}
	if len(owns) == 0 {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: --paths is required\n")
		claimEditUsage(os.Stderr)
		os.Exit(2)
	}
	for _, p := range owns {
		if err := claimPathError(p); err != "" {
			fmt.Fprintf(os.Stderr, "flywheel claim-edit: %s\n", err)
			claimEditUsage(os.Stderr)
			os.Exit(2)
		}
	}
	if err := flywheel.AppendEvent(o.dir, flywheel.Event{Kind: "lead_edit", Session: o.session, Owns: owns, Baseline: claimBaseline(o.dir, owns), Note: o.note}); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: %v\n", err)
		os.Exit(1)
	}
	if _, err := flywheel.WriteState(o.dir); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("claimed lead edit: %s -> session %s\n", strings.Join(owns, ", "), o.session)
}

// claimPathError returns the usage error for a claim path that is not a
// literal file, or "" when p is fine to claim. A claim must name the files
// actually edited: a path with a path.Match metacharacter (*, ?, [) or a
// trailing "/" directory prefix would let ownsContains exempt a whole tree,
// and a pattern cannot be bound to one content hash anyway, which is the
// deeper reason (issue #258).
func claimPathError(p string) string {
	if strings.ContainsAny(p, "*?[") {
		return fmt.Sprintf("--paths %q is not a literal path; claims must name the files actually edited", p)
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Sprintf("--paths %q is a directory prefix; claims must name the files actually edited", p)
	}
	return ""
}

// claimBaseline hashes every claimed path's content in dir at this moment:
// path -> sha256 hex, or "deleted" when the path does not exist. It mirrors
// internal/flywheel's fileSHA reading, which is exactly what the owns check
// compares against, so the claim is bound to the edit that was actually
// declared: a later change to the path makes it outside again (issue #258).
func claimBaseline(dir string, paths []string) map[string]string {
	base := make(map[string]string, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			base[p] = "deleted"
			continue
		}
		sum := sha256.Sum256(b)
		base[p] = hex.EncodeToString(sum[:])
	}
	return base
}
