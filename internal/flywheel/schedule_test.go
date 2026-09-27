package flywheel

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestScheduleNameStableAndUniquePerPath checks the task name is the same
// for one path every time and differs for two checkouts sharing a base name.
func TestScheduleNameStableAndUniquePerPath(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir()+"/repo", t.TempDir()+"/repo"
	pa, err := planFor(a, "fw", 0)
	if err != nil {
		t.Fatal(err)
	}
	pa2, _ := planFor(a, "fw", 0)
	pb, _ := planFor(b, "fw", 0)
	if pa.Name != pa2.Name {
		t.Errorf("name not stable: %q vs %q", pa.Name, pa2.Name)
	}
	if pa.Name == pb.Name {
		t.Errorf("two paths share name %q", pa.Name)
	}
	if !strings.HasPrefix(pa.Name, "flywheel-repo-") || len(pa.Name) != len("flywheel-repo-")+8 {
		t.Errorf("name = %q, want flywheel-repo-<8 hex>", pa.Name)
	}
	if got := ScheduleName("/x/my repo"); !strings.HasPrefix(got, "flywheel-my_repo-") {
		t.Errorf("ScheduleName with a space = %q, want the space replaced", got)
	}
}

// TestSchedulePlanEveryAndArgs checks the default interval, the one-minute
// floor and the controller argv.
func TestSchedulePlanEveryAndArgs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p, err := planFor(dir, "fw", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Every != 15*time.Minute {
		t.Errorf("default Every = %v, want 15m", p.Every)
	}
	if want := []string{"controller", "--once", "--dir", p.Dir}; !reflect.DeepEqual(p.Args, want) {
		t.Errorf("Args = %v, want %v", p.Args, want)
	}
	for _, e := range []time.Duration{30 * time.Second, -time.Minute} {
		if _, err := planFor(dir, "fw", e); err == nil {
			t.Errorf("planFor(every=%v) accepted, want refused", e)
		}
	}
	if p, _ := planFor(dir, "fw", 90*time.Second); p.Every != time.Minute {
		t.Errorf("90s truncated to %v, want 1m", p.Every)
	}
}

// schedPlan is a fixed plan with spaces in both paths.
var schedPlan = SchedulePlan{Name: "flywheel-r-0011aabb", Every: 5 * time.Minute, Exe: `C:\Program Files\fw.exe`,
	Dir: `C:\my repo`, Args: []string{"controller", "--once", "--dir", `C:\my repo`}}

// TestScheduleSchtasksArgs checks the Windows argv for install, remove and
// status, with paths holding spaces quoted inside /TR.
func TestScheduleSchtasksArgs(t *testing.T) {
	t.Parallel()
	want := map[string][]string{
		"install": {"/Create", "/F", "/SC", "MINUTE", "/MO", "5", "/TN", schedPlan.Name, "/TR",
			`"C:\Program Files\fw.exe" controller --once --dir "C:\my repo"`},
		"remove": {"/Delete", "/F", "/TN", schedPlan.Name},
		"status": {"/Query", "/TN", schedPlan.Name, "/FO", "LIST"},
	}
	for op, w := range want {
		if got := schtasksArgs(op, schedPlan); !reflect.DeepEqual(got, w) {
			t.Errorf("schtasksArgs(%s) = %q, want %q", op, got, w)
		}
	}
}

// TestScheduleCrontabKeepsOtherLines checks install adds exactly one marked
// entry and keeps unrelated lines, reinstall replaces it, remove drops only it.
func TestScheduleCrontabKeepsOtherLines(t *testing.T) {
	t.Parallel()
	orig := "MAILTO=me\n0 * * * * backup.sh\n"
	got, err := cronInstall(orig, schedPlan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, orig) || strings.Count(got, cronMarker(schedPlan.Name)) != 1 {
		t.Fatalf("install = %q, want the original lines then one marked entry", got)
	}
	entry, _ := cronEntry(schedPlan)
	if !strings.HasPrefix(entry, "*/5 * * * * 'C:\\Program Files\\fw.exe' controller") || !strings.HasSuffix(entry, " 2>&1") {
		t.Errorf("entry = %q", entry)
	}
	p2 := schedPlan
	p2.Every = 2 * time.Hour
	again, err := cronInstall(got, p2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(again, cronMarker(schedPlan.Name)) != 1 || !strings.Contains(again, "0 */2 * * * ") || strings.Contains(again, "*/5 ") {
		t.Errorf("reinstall = %q, want one entry at 2h", again)
	}
	if ok, line := cronFind(again, schedPlan.Name); !ok || !strings.HasPrefix(line, "0 */2") {
		t.Errorf("cronFind = %v %q", ok, line)
	}
	if rm := cronRemove(again, schedPlan.Name); rm != orig {
		t.Errorf("remove = %q, want %q", rm, orig)
	}
	if ok, _ := cronFind(orig, schedPlan.Name); ok {
		t.Error("cronFind found an entry in a crontab without one")
	}
	p2.Every = 90 * time.Minute
	if _, err := cronInstall(orig, p2); err == nil {
		t.Error("cron accepted 90m, which */N cannot express")
	}
}

// TestScheduleLaunchdPlist checks the plist's label, StartInterval and
// ProgramArguments.
func TestScheduleLaunchdPlist(t *testing.T) {
	t.Parallel()
	got := launchdPlist(schedPlan)
	for _, w := range []string{
		"<string>io.github.suzworx." + schedPlan.Name + "</string>",
		"<key>StartInterval</key>\n\t<integer>300</integer>",
		"<key>ProgramArguments</key>\n\t<array>\n\t\t<string>C:\\Program Files\\fw.exe</string>\n\t\t<string>controller</string>\n\t\t<string>--once</string>\n\t\t<string>--dir</string>\n\t\t<string>C:\\my repo</string>\n\t</array>",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("plist missing %q:\n%s", w, got)
		}
	}
}

// TestScheduleRunnerErrorNamesCommand checks a failing command's error names
// the command and carries its output.
func TestScheduleRunnerErrorNamesCommand(t *testing.T) {
	t.Parallel()
	fail := func(string, ...string) ([]byte, error) {
		return []byte("ERROR: access denied\n"), errors.New("exit status 1")
	}
	_, err := runSched(fail, "schtasks", "/Delete", "/F")
	if err == nil || !strings.Contains(err.Error(), "schtasks /Delete /F") || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("err = %v, want the command and its output", err)
	}
}
