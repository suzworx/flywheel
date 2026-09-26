package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// ledgerUsageLine is ledger's usage line, shared by help and errors.
const ledgerUsageLine = "flywheel ledger backup <path> [--dir DIR] [--json]"

func init() {
	register("ledger", "keep the ledger safe\n    backup <path>      write a verified, point-in-time copy of .flywheel/ to <path>", runLedger)
	registerHelp("ledger", ledgerUsageLine, func() *flag.FlagSet { fs, _ := ledgerBackupFlags(); return fs })
}

// ledgerBackupOptions holds the parsed ledger backup flags.
type ledgerBackupOptions struct {
	dir  string
	json bool
}

// ledgerBackupFlags defines ledger backup's flags once, so help and run share them.
func ledgerBackupFlags() (*flag.FlagSet, *ledgerBackupOptions) {
	fs := flag.NewFlagSet("ledger backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &ledgerBackupOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.json, "json", false, "print the result as JSON")
	return fs, o
}

func ledgerUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: "+ledgerUsageLine)
}

// runLedger implements `flywheel ledger <subcommand>`; backup is the only one.
func runLedger(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "flywheel ledger: missing subcommand (backup)")
		ledgerUsage(os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "backup":
		runLedgerBackup(args[1:])
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
