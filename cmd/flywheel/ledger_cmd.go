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

// ledgerUsageLine is ledger's usage: backup and move (issue #464), shared by
// help and errors.
const ledgerUsageLine = "flywheel ledger backup <path> [--dir DIR] [--json]\n" +
	"       flywheel ledger move [--dir DIR] [--json]"

func init() {
	register("ledger", "keep the ledger safe\n    backup <path>      write a verified, point-in-time copy of .flywheel/ to <path>\n    move               move the in-tree event log into ledger.dir, verified, then remove it from .flywheel/", runLedger)
	registerHelp("ledger", ledgerUsageLine, func() *flag.FlagSet { fs, _ := ledgerBackupFlags(); return fs })
}

// ledgerBackupFlags is ledger backup's flag set, the one help shows.
func ledgerBackupFlags() (*flag.FlagSet, *ledgerOptions) { return ledgerFlags("ledger backup") }

// ledgerOptions holds the parsed ledger backup and move flags.
type ledgerOptions struct {
	dir  string
	json bool
}

// ledgerFlags defines the flags backup and move share once, so help and run
// share them.
func ledgerFlags(name string) (*flag.FlagSet, *ledgerOptions) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &ledgerOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.json, "json", false, "print the result as JSON")
	return fs, o
}

func ledgerUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: "+ledgerUsageLine)
}

// runLedger implements `flywheel ledger <subcommand>`: backup or move.
func runLedger(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "flywheel ledger: missing subcommand (backup, move)")
		ledgerUsage(os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "backup":
		runLedgerBackup(args[1:])
	case "move":
		runLedgerMove(args[1:])
	case "-h", "--help":
		ledgerUsage(os.Stderr)
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "flywheel ledger: unknown subcommand %q\n", args[0])
		ledgerUsage(os.Stderr)
		os.Exit(2)
	}
}

// runLedgerBackup implements `flywheel ledger backup <path>` (issue #464):
// a verified copy of the ledger, so nobody has to commit it to git to keep
// it. Exit 2 on a usage error, 1 on any other error; a broken chain is
// copied and warned about, not an error.
func runLedgerBackup(args []string) {
	fs, o := ledgerBackupFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel ledger backup: %v\n", err)
		ledgerUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "flywheel ledger backup: exactly one destination path is required")
		ledgerUsage(os.Stderr)
		os.Exit(2)
	}
	res, err := flywheel.Backup(o.dir, pos[0], time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel ledger backup: %v\n", err)
		os.Exit(1)
	}
	if o.json {
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel ledger backup: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
	} else {
		chain := fmt.Sprintf("chain ok (%d lines)", res.ChainLines)
		if !res.ChainOK {
			chain = "chain BROKEN at " + res.ChainBreak
		}
		fmt.Printf("backed up %d files (%d bytes) to %s; %s\n", res.Files, res.Bytes, res.Dest, chain)
	}
	if !res.ChainOK {
		fmt.Fprintln(os.Stderr, "warning: the ledger's chain is broken; run flywheel verify")
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintf(os.Stderr, "skipped %d paths\n", len(res.Skipped))
	}
	if res.PartialTailBytes > 0 {
		fmt.Fprintf(os.Stderr, "left out a partial last line (%d bytes)\n", res.PartialTailBytes)
	}
}

// runLedgerMove implements `flywheel ledger move` (issue #464): the verified
// migration of the in-tree event log into ledger.dir. Exit 2 on a usage error
// (ledger.dir unset or refused), 6 when the destination holds another log
// (rule two-live-logs), 1 on any other error.
func runLedgerMove(args []string) {
	fs, o := ledgerFlags("ledger move")
	pos, err := parseArgs(fs, args)
	if err == nil && len(pos) > 0 {
		err = fmt.Errorf("unexpected argument %q", pos[0])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel ledger move: %v\n", err)
		ledgerUsage(os.Stderr)
		os.Exit(2)
	}
	res, err := flywheel.LedgerMove(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel ledger move: %v\n", err)
		var usage *flywheel.LedgerUsageError
		switch {
		case errors.As(err, &usage):
			ledgerUsage(os.Stderr)
			os.Exit(2)
		case flywheel.IsRuleRefusal(err):
			os.Exit(6)
		}
		os.Exit(1)
	}
	if o.json {
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel ledger move: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
	} else if !res.Moved {
		fmt.Println(res.Note)
	} else {
		verb, chain := "moved", "chain ok"
		if res.Resumed {
			verb = "resumed"
		}
		if !res.ChainOK {
			chain = "chain BROKEN"
		}
		fmt.Printf("%s %d files (%d bytes) from %s to %s; %s\n", verb, len(res.Files), res.Bytes, res.Source, res.Destination, chain)
	}
	if res.Moved && !res.ChainOK {
		fmt.Fprintln(os.Stderr, "warning: the moved log's chain is broken; run flywheel verify")
	}
	if res.Tracked {
		fmt.Fprintln(os.Stderr, "the in-tree log was tracked by git: commit its removal (git rm --cached is not run for you)")
	}
}
