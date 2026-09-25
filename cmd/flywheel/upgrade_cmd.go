package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("upgrade", "update flywheel to a release, with checksum verification", runUpgrade)
	registerHelp("upgrade", "flywheel upgrade [--check] [--to VERSION] [--repo REPO] [--dir DIR] [--force]", func() *flag.FlagSet { fs, _ := upgradeFlags(); return fs })
}

// upgradeOptions holds the parsed `flywheel upgrade` flags.
type upgradeOptions struct {
	check  bool
	to     string
	repo   string
	api    string
	dl     string
	goos   string
	goarch string
	dest   string
	dir    string
	force  bool
}

// upgradeUsage prints the flywheel upgrade usage line.
func upgradeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel upgrade [--check] [--to VERSION] [--repo REPO] [--dir DIR] [--force]")
}

// upgradeFlags defines upgrade's flags once, so help and run share them.
func upgradeFlags() (*flag.FlagSet, *upgradeOptions) {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &upgradeOptions{}
	fs.BoolVar(&o.check, "check", false, "print current and latest versions and whether an upgrade is available")
	fs.StringVar(&o.to, "to", "", "install this version instead of the latest (e.g. v0.11.0)")
	fs.StringVar(&o.repo, "repo", "suzworx/flywheel", "owner/repo to upgrade from")
	fs.StringVar(&o.api, "api", "https://api.github.com", "GitHub API base")
	fs.StringVar(&o.dl, "download", "https://github.com", "download base")
	fs.StringVar(&o.goos, "goos", "", "target GOOS (default the host)")
	fs.StringVar(&o.goarch, "goarch", "", "target GOARCH (default the host)")
	fs.StringVar(&o.dest, "dest", "", "binary path to replace (default the running executable)")
	fs.StringVar(&o.dir, "dir", ".", "project directory whose live run leases block the upgrade")
	fs.BoolVar(&o.force, "force", false, "upgrade even while a run's lease is live in --dir (prints a warning)")
	return fs, o
}

// opts maps the parsed flags onto the upgrade options the package drives.
func (o *upgradeOptions) opts() flywheel.UpgradeOptions {
	return flywheel.UpgradeOptions{
		APIBase:      o.api,
		DownloadBase: o.dl,
		Repo:         o.repo,
		Version:      o.to,
		GOOS:         o.goos,
		GOARCH:       o.goarch,
		Dest:         o.dest,
	}
}

// runUpgrade implements `flywheel upgrade`: with --check it prints the
// current and latest versions and whether an upgrade is available (exit 0
// either way); otherwise it downloads, verifies and installs the release.
// Before installing it refuses while a run's lease is live in --dir, since
// swapping the binary under a live run can crash it (issue #461); --force
// overrides with a warning. Exit 0 on success, 1 on a failed upgrade
// (nothing installed on a checksum mismatch), 2 on a usage error, 6 on a
// refusal for live runs.
func runUpgrade(args []string) {
	fs, o := upgradeFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel upgrade: %v\n", err)
		upgradeUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel upgrade: unexpected argument %q\n", pos[0])
		upgradeUsage(os.Stderr)
		os.Exit(2)
	}
	opts := o.opts()
	if o.check {
		latest, available, err := flywheel.CheckUpgrade(version, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel upgrade: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("current: %s\n", version)
		fmt.Printf("latest: %s\n", latest)
		if available {
			fmt.Println("upgrade available")
		} else {
			fmt.Println("up to date")
		}
		return
	}
	live, err := flywheel.LiveLeases(o.dir, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel upgrade: check live runs in %s: %v\n", o.dir, err)
		os.Exit(1)
	}
	if len(live) > 0 {
		names := make([]string, len(live))
		for i, l := range live {
			names[i] = fmt.Sprintf("%s %s (pid %d, expires %s)", l.Task, l.Attempt, l.PID, l.ExpiresAt)
		}
		if !o.force {
			fmt.Fprintf(os.Stderr, "flywheel upgrade: refused: %d run(s) live in %s: %s; wait for them (flywheel wait) or pass --force\n",
				len(live), o.dir, strings.Join(names, ", "))
			os.Exit(6)
		}
		fmt.Fprintf(os.Stderr, "flywheel upgrade: warning: --force: upgrading under %d live run(s) in %s: %s\n",
			len(live), o.dir, strings.Join(names, ", "))
	}
	installed, err := flywheel.Upgrade(version, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel upgrade: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("upgraded %s -> %s\n", version, installed)
}
