//go:build !windows

package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// NewScheduler returns the host's Scheduler, running its commands through
// run: launchd (a LaunchAgent in ~/Library/LaunchAgents) on macOS, the user
// crontab elsewhere.
func NewScheduler(run Runner) (Scheduler, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("schedule: %v", err)
		}
		return launchdScheduler{run: run, agents: filepath.Join(home, "Library", "LaunchAgents")}, nil
	}
	return cronScheduler{run: run}, nil
}

// cronScheduler keeps each plan as a marked entry in the user crontab and
// never touches the other lines.
type cronScheduler struct{ run Runner }

// read returns the current crontab; no crontab yet reads as empty.
func (c cronScheduler) read() (string, error) {
	out, err := runSched(c.run, "crontab", "-l")
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no crontab") {
			return "", nil
		}
		return "", err
	}
	return string(out), nil
}

// write installs content as the crontab through a temporary file.
func (c cronScheduler) write(content string) error {
	f, err := os.CreateTemp("", "flywheel-crontab-*")
	if err != nil {
		return fmt.Errorf("schedule: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return fmt.Errorf("schedule: %v", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("schedule: %v", err)
	}
	_, err = runSched(c.run, "crontab", f.Name())
	return err
}

// Install replaces or adds the plan's entry.
func (c cronScheduler) Install(p SchedulePlan) error {
	cur, err := c.read()
	if err != nil {
		return err
	}
	next, err := cronInstall(cur, p)
	if err != nil {
		return err
	}
	return c.write(next)
}

// Remove deletes the plan's entry; a missing one is not an error.
func (c cronScheduler) Remove(name string) error {
	cur, err := c.read()
	if err != nil {
		return err
	}
	if ok, _ := cronFind(cur, name); !ok {
		return nil
	}
	return c.write(cronRemove(cur, name))
}

// Status finds the plan's entry and returns its line.
func (c cronScheduler) Status(name string) (bool, string, error) {
	cur, err := c.read()
	if err != nil {
		return false, "", err
	}
	ok, line := cronFind(cur, name)
	return ok, line, nil
}

// launchdScheduler writes a LaunchAgent plist per plan into agents.
type launchdScheduler struct {
	run    Runner
	agents string
}

// plist is the plist path for a task name.
func (l launchdScheduler) plist(name string) string {
	return filepath.Join(l.agents, launchdLabel(name)+".plist")
}

// Install writes the plist and loads it, unloading any earlier copy first.
func (l launchdScheduler) Install(p SchedulePlan) error {
	path := l.plist(p.Name)
	if _, err := os.Stat(path); err == nil {
		_, _ = l.run("launchctl", "unload", path)
	}
	if err := os.MkdirAll(l.agents, 0o755); err != nil {
		return fmt.Errorf("schedule: %v", err)
	}
	if err := os.WriteFile(path, []byte(launchdPlist(p)), 0o644); err != nil {
		return fmt.Errorf("schedule: %v", err)
	}
	_, err := runSched(l.run, "launchctl", "load", "-w", path)
	return err
}

// Remove unloads and deletes the plist; a missing one is not an error.
func (l launchdScheduler) Remove(name string) error {
	path := l.plist(name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	if _, err := runSched(l.run, "launchctl", "unload", "-w", path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("schedule: %v", err)
	}
	return nil
}

// Status is installed when the plist exists; the detail is launchctl's
// listing of the label, or why it is not loaded.
func (l launchdScheduler) Status(name string) (bool, string, error) {
	path := l.plist(name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, "no " + path, nil
	}
	out, err := runSched(l.run, "launchctl", "list", launchdLabel(name))
	if err != nil {
		return true, path + " present but not loaded: " + err.Error(), nil
	}
	return true, path + "\n" + strings.TrimSpace(string(out)), nil
}
