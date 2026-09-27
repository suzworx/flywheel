//go:build windows

package flywheel

import (
	"strings"
)

// NewScheduler returns the host's Scheduler: Task Scheduler (schtasks) on
// Windows, running its commands through run.
func NewScheduler(run Runner) (Scheduler, error) {
	return schtasksScheduler{run: run}, nil
}

// schtasksScheduler registers plans as Task Scheduler tasks.
type schtasksScheduler struct{ run Runner }

// Install creates or replaces (/F) the task.
func (s schtasksScheduler) Install(p SchedulePlan) error {
	_, err := runSched(s.run, "schtasks", schtasksArgs("install", p)...)
	return err
}

// Remove deletes the task.
func (s schtasksScheduler) Remove(name string) error {
	_, err := runSched(s.run, "schtasks", schtasksArgs("remove", SchedulePlan{Name: name})...)
	return err
}

// Status queries the task; a task schtasks cannot find is not installed,
// any other failure is an error.
func (s schtasksScheduler) Status(name string) (bool, string, error) {
	out, err := runSched(s.run, "schtasks", schtasksArgs("status", SchedulePlan{Name: name})...)
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "cannot find") {
			return false, strings.TrimSpace(string(out)), nil
		}
		return false, "", err
	}
	return true, strings.TrimSpace(string(out)), nil
}
