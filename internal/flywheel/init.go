package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const markdownTemplate = `## Status

status: initialized

<!-- flywheel:status:start -->
<!-- flywheel:status:end -->

## Main session

Placeholder: main session notes go here.

## Workers

Placeholder: worker state and briefs go here.

## Roles

Placeholder: role assignments go here.

## Task log

Placeholder: completed tasks are logged here.
`

// ScaffoldPiece reports the status of one piece of the on-disk scaffold
// InitSeeded manages. Pieces are always returned in the same fixed order:
// flywheel.md, then the six pieces under .flywheel/ (state.json,
// events.jsonl, config.json, .gitignore, .gitattributes, briefs/), then
// AGENTS.md last, when --agents-md requested it.
type ScaffoldPiece struct {
	Path  string // relative to dir, forward-slash (e.g. ".flywheel/state.json")
	Added bool   // this call created it, or (flywheel.md only) force-reset it
}

// Init scaffolds flywheel state files into dir. It returns the absolute path
// of the initialized directory.
//
// Init never refuses because something already exists: each scaffold piece
// is filled in independently when missing and left byte-identical when
// present. flywheel.md is the one exception — with force, an existing
// regular file there is replaced with the built-in template; directories and
// symlinks are rejected as its destination either way. See InitSeeded for
// the full piece-by-piece contract.
//
// Init is not a crash-atomic multi-file transaction. Payloads are staged
// before any write, and a returned error rolls back only what this call
// itself created or overwrote (a force-reset flywheel.md's preexisting bytes
// are restored, other pieces this call created are removed, directories
// created by this call are removed when empty). Unrelated files are never
// touched. A crash mid-write can still leave partial state; retries are
// always safe.
func Init(dir string, force bool) (string, error) {
	path, _, err := InitSeeded(dir, force, "", "", false)
	return path, err
}

// InitSeeded is Init with optional model and variant seeding and an
// optional AGENTS.md piece, and reports the status of every scaffold piece
// instead of just Init's path and error.
//
// Every piece — flywheel.md and, under .flywheel/, state.json,
// events.jsonl, config.json, .gitignore, .gitattributes and briefs/ — is
// handled independently: a missing piece is created, and one that already
// exists is left byte-identical and reported present. Running init again
// after an upgrade (for example one that adds .gitattributes to the
// scaffold) fills in only what's new, without touching anything else,
// whether or not flywheel.md already exists.
//
// flywheel.md is the one piece force affects: with force, an existing
// regular file there is replaced with the built-in template. Every other
// piece is created only if missing and never overwritten, with or without
// force. model and variant seed a freshly created config.json's default
// worker (validated through WriteConfig); an existing config.json is never
// touched.
//
// agentsMD, when true, adds one more piece: <dir>/AGENTS.md carries a block
// between markers naming the installed skills and the persona each plays.
// A missing AGENTS.md is created with just that block; an existing one has
// exactly its previous marked block replaced (or the block appended, if it
// never had one), and the rest of the file is left alone. Byte-identical
// content is reported present, like every other piece, so a rerun with
// --agents-md over an already-current file never reports a change.
func InitSeeded(dir string, force bool, model, variant string, agentsMD bool) (string, []ScaffoldPiece, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %q: %w", dir, err)
	}

	mdPath := filepath.Join(abs, "flywheel.md")
	dotFlywheel := filepath.Join(abs, ".flywheel")
	briefsDir := filepath.Join(dotFlywheel, "briefs")
	statePath := filepath.Join(dotFlywheel, "state.json")
	eventsPath := filepath.Join(dotFlywheel, "events.jsonl")
	gitignorePath := filepath.Join(dotFlywheel, ".gitignore")
	gitattributesPath := filepath.Join(dotFlywheel, ".gitattributes")
	configPath := filepath.Join(dotFlywheel, configFileName)

	agentsMDPath := filepath.Join(abs, "AGENTS.md")

	if err := preflightRegularFile(mdPath); err != nil {
		return "", nil, err
	}
	if agentsMD {
		if err := preflightRegularFile(agentsMDPath); err != nil {
			return "", nil, err
		}
	}

	// Stage the payloads before publishing anything.
	mdBytes := []byte(markdownTemplate)
	stateJSON, err := json.MarshalIndent(Derive([]Event{}), "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode state: %w", err)
	}
	stateBytes := append(stateJSON, '\n')
	cfg := DefaultConfig()
	if model != "" {
		cfg.Workers[0].Model = model
	}
	if variant != "" {
		cfg.Workers[0].Variant = variant
	}
	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode config: %w", err)
	}
	configBytes := append(configJSON, '\n')

	// Snapshot what existed before this call so a failed force-reset can
	// restore flywheel.md's preexisting bytes, and a rollback can remove only
	// what this call created.
	mdExisted, mdPrev := snapshotFile(mdPath)
	briefsExisted := dirExisted(briefsDir)
	dotFlywheelExisted := dirExisted(dotFlywheel)
	var mdAdded, createdState, createdEvents, createdGitignore, createdGitattributes, createdConfig bool
	var agentsMDWritten, agentsMDExisted bool
	var agentsMDPrev []byte

	// rollback undoes this call's own footprint after an error: restore
	// flywheel.md's (and AGENTS.md's) preexisting bytes if this call changed
	// them, remove pieces this call created, and remove directories this
	// call created (only if empty, never recursive).
	rollback := func() {
		if mdAdded {
			if mdExisted {
				_ = os.WriteFile(mdPath, mdPrev, 0o644)
			} else {
				_ = os.Remove(mdPath)
			}
		}
		if agentsMDWritten {
			if agentsMDExisted {
				_ = os.WriteFile(agentsMDPath, agentsMDPrev, 0o644)
			} else {
				_ = os.Remove(agentsMDPath)
			}
		}
		if createdState {
			_ = os.Remove(statePath)
		}
		if createdEvents {
			_ = os.Remove(eventsPath)
		}
		if createdGitignore {
			_ = os.Remove(gitignorePath)
		}
		if createdGitattributes {
			_ = os.Remove(gitattributesPath)
		}
		if createdConfig {
			_ = os.Remove(configPath)
		}
		if !briefsExisted {
			_ = os.Remove(briefsDir)
		}
		if !dotFlywheelExisted {
			_ = os.Remove(dotFlywheel)
		}
	}

	if err := os.MkdirAll(briefsDir, 0o755); err != nil {
		rollback()
		return "", nil, fmt.Errorf("create %s: %w", briefsDir, err)
	}

	mdAdded, err = publishMarkdown(mdPath, mdBytes, force)
	if err != nil {
		rollback()
		return "", nil, fmt.Errorf("write %s: %w", mdPath, err)
	}

	// --agents-md is a piece like any other: created when AGENTS.md is
	// missing, and updated in place — replacing exactly the previous marked
	// block, or appending a fresh one when none is found — when it already
	// exists. Byte-identical content is left unwritten and reported present.
	if agentsMD {
		agentsMDExisted, agentsMDPrev = snapshotFile(agentsMDPath)
		next := agentsMDMerged(agentsMDExisted, agentsMDPrev)
		if !agentsMDExisted || string(agentsMDPrev) != string(next) {
			if err := os.WriteFile(agentsMDPath, next, 0o644); err != nil {
				rollback()
				return "", nil, fmt.Errorf("write %s: %w", agentsMDPath, err)
			}
			agentsMDWritten = true
		}
	}

	// Everything below is created only if missing and never overwritten or
	// truncated, with or without force: state.json and events.jsonl are the
	// project's source of truth, and config.json/.gitignore/.gitattributes
	// are left for the caller to edit once they exist.
	createdState, err = createIfMissing(statePath, stateBytes)
	if err != nil {
		rollback()
		return "", nil, fmt.Errorf("write %s: %w", statePath, err)
	}
	createdEvents, err = createIfMissing(eventsPath, []byte{})
	if err != nil {
		rollback()
		return "", nil, fmt.Errorf("write %s: %w", eventsPath, err)
	}
	// --model and --variant seed a fresh config through WriteConfig; an
	// existing one is left untouched for the caller to edit.
	if model != "" || variant != "" {
		if !regularFileExists(configPath) {
			if err := WriteConfig(dir, cfg); err != nil {
				rollback()
				return "", nil, fmt.Errorf("write %s: %w", configPath, err)
			}
			createdConfig = true
		}
	} else {
		createdConfig, err = createIfMissing(configPath, configBytes)
		if err != nil {
			rollback()
			return "", nil, fmt.Errorf("write %s: %w", configPath, err)
		}
	}
	createdGitignore, err = createIfMissing(gitignorePath, []byte("runs/\nworktrees/\nlocks/\n"))
	if err != nil {
		rollback()
		return "", nil, fmt.Errorf("write %s: %w", gitignorePath, err)
	}
	createdGitattributes, err = createIfMissing(gitattributesPath, []byte("* text eol=lf\n"))
	if err != nil {
		rollback()
		return "", nil, fmt.Errorf("write %s: %w", gitattributesPath, err)
	}

	pieces := []ScaffoldPiece{
		{Path: "flywheel.md", Added: mdAdded},
		{Path: ".flywheel/state.json", Added: createdState},
		{Path: ".flywheel/events.jsonl", Added: createdEvents},
		{Path: ".flywheel/config.json", Added: createdConfig},
		{Path: ".flywheel/.gitignore", Added: createdGitignore},
		{Path: ".flywheel/.gitattributes", Added: createdGitattributes},
		{Path: ".flywheel/briefs/", Added: !briefsExisted},
	}
	if agentsMD {
		pieces = append(pieces, ScaffoldPiece{Path: "AGENTS.md", Added: agentsMDWritten})
	}

	return abs, pieces, nil
}

// preflightRegularFile checks that path is a usable scaffold destination —
// absent, or an existing regular file — before InitSeeded touches anything.
// A directory or symlink there is refused unconditionally: a fresh write
// would go through it, and a force reset (flywheel.md only) must not write
// through a symlink. Used for both flywheel.md and, when --agents-md is
// requested, AGENTS.md.
func preflightRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("check %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to write through it", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; refusing to overwrite it", path)
	}
	return nil
}

// publishMarkdown creates flywheel.md if it is missing. If it already exists
// as a regular file (preflightMarkdown must have confirmed that), force
// replaces its bytes and non-force leaves it untouched. It reports whether
// this call wrote to the file — freshly created, or force-reset — so the
// caller can label it "added" and a later failure can be rolled back.
func publishMarkdown(path string, b []byte, force bool) (added bool, err error) {
	if force {
		if _, err := os.Lstat(path); err == nil {
			return true, os.WriteFile(path, b, 0o644)
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("check %s: %w", path, err)
		}
	}
	return createIfMissing(path, b)
}

// createIfMissing writes b to path only when path does not exist, using
// O_EXCL so a racing creator wins and the file is never overwritten or
// truncated. It reports whether this call created the file.
func createIfMissing(path string, b []byte) (created bool, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	if len(b) > 0 {
		if _, err := f.Write(b); err != nil {
			f.Close()
			os.Remove(path)
			return true, err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return true, err
	}
	return true, nil
}

// snapshotFile returns whether p existed as a regular file before Init ran
// and, if so, its exact bytes, so a failed force update can restore them.
func snapshotFile(p string) (existed bool, prev []byte) {
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return false, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return true, nil // exists but unreadable; restore as best effort
	}
	return true, b
}

// agentsMDStart and agentsMDEnd delimit the block InitSeeded writes into
// AGENTS.md for --agents-md: a pointer to installing the skills, and the
// persona each one plays for a lead that reads AGENTS.md instead of loading
// skills directly. A rerun replaces exactly this block and leaves the rest
// of the file alone.
const (
	agentsMDStart = "<!-- flywheel:agents:start -->"
	agentsMDEnd   = "<!-- flywheel:agents:end -->"
)

// agentsMDPersonas lists, in the order printed, every skill folder shipped
// with flywheel and the persona it plays.
var agentsMDPersonas = [][2]string{
	{"flywheel", "lead"},
	{"flywheel-operator", "operator"},
	{"flywheel-foreman", "foreman"},
	{"flywheel-inspector", "inspector"},
	{"flywheel-auditor", "auditor"},
	{"flywheel-planner", "planner"},
	{"flywheel-steward", "steward"},
	{"flywheel-worker", "worker"},
}

// agentsMDBlock returns the exact bytes InitSeeded writes between
// agentsMDStart and agentsMDEnd: a short pointer to installing the skills,
// then one "- skill: persona" line per entry in agentsMDPersonas.
func agentsMDBlock() []byte {
	var b strings.Builder
	b.WriteString(agentsMDStart + "\n")
	b.WriteString("flywheel: a factory for AI coding agents. Install the skills with\n")
	b.WriteString("`npx skills add suzworx/flywheel --skill <name>`, or copy the skill folders directly.\n\n")
	for _, p := range agentsMDPersonas {
		fmt.Fprintf(&b, "- %s: %s\n", p[0], p[1])
	}
	b.WriteString(agentsMDEnd + "\n")
	return []byte(b.String())
}

// agentsMDMerged returns what AGENTS.md should contain after applying
// InitSeeded's block to prev (existed reports whether prev came from a real
// file). A missing file gets just the block. An existing file with a
// well-formed agentsMDStart/agentsMDEnd pair has exactly that pair replaced
// (its own trailing newline consumed, so a rerun never grows blank lines);
// content outside the markers is untouched. An existing file with no
// well-formed pair gets the block appended after its own content.
func agentsMDMerged(existed bool, prev []byte) []byte {
	block := agentsMDBlock()
	if !existed {
		return block
	}
	s := string(prev)
	if start := strings.Index(s, agentsMDStart); start >= 0 {
		if rel := strings.Index(s[start:], agentsMDEnd); rel >= 0 {
			end := start + rel + len(agentsMDEnd)
			if end < len(s) && s[end] == '\n' {
				end++
			}
			return []byte(s[:start] + string(block) + s[end:])
		}
	}
	out := append([]byte(nil), prev...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	if len(out) > 0 {
		out = append(out, '\n')
	}
	return append(out, block...)
}

// dirExisted reports whether p existed before Init ran; used only to decide
// whether a rollback may attempt to remove it (removal is best-effort and
// only succeeds when the directory is empty).
func dirExisted(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// regularFileExists reports whether p exists as a regular file, so the
// idempotent re-init check refuses to treat directories or symlinks at a
// scaffold path as "already initialized".
func regularFileExists(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode().IsRegular()
}

// IgnoredStateFiles lists which of the state files init creates git would
// ignore, so callers can warn that they would never be committed. Each
// returned path is relative to dir (for example ".flywheel/events.jsonl").
// Not a git work tree, or git missing, reports nothing: those are
// non-issues, not errors.
func IgnoredStateFiles(dir string) []string {
	var ignored []string
	for _, name := range []string{"events.jsonl", "state.json", configFileName} {
		p := filepath.ToSlash(filepath.Join(".flywheel", name))
		if gitIgnores(dir, p) {
			ignored = append(ignored, p)
		}
	}
	// The shards: the floor shard always, and every shard that exists (up to
	// a bound), because an ignore rule can name one file and leave the rest
	// visible (#351 review).
	probes := []string{filepath.ToSlash(filepath.Join(".flywheel", "events", "@floor.jsonl"))}
	if entries, err := os.ReadDir(filepath.Join(dir, ".flywheel", "events")); err == nil {
		for _, e := range entries {
			if len(probes) >= maxShardProbes {
				break
			}
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || e.Name() == floorShard {
				continue
			}
			probes = append(probes, filepath.ToSlash(filepath.Join(".flywheel", "events", e.Name())))
		}
	}
	for _, p := range probes {
		if gitIgnores(dir, p) {
			ignored = append(ignored, p)
		}
	}
	return ignored
}

// maxShardProbes bounds how many shard files IgnoredStateFiles asks git
// about: enough to catch a rule that hides them, cheap on a large factory.
const maxShardProbes = 20

// EnsureLocksIgnored adds a "locks/" line to an existing .flywheel/.gitignore
// when it has none, so a migrated repository ignores the transient shard
// locks the way a fresh one does (#351 review). A missing file is left to
// Init; the file's existing content and newline style are kept.
func EnsureLocksIgnored(dir string) error {
	p := filepath.Join(dir, ".flywheel", ".gitignore")
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", p, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(strings.TrimSuffix(line, "\r")) == "locks/" {
			return nil
		}
	}
	nl := "\n"
	if strings.Contains(string(b), "\r\n") {
		nl = "\r\n"
	}
	out := string(b)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += nl
	}
	out += "locks/" + nl
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

// IgnoreMarkdown ensures the root .gitignore in dir contains an exact
// "flywheel.md" line, so a tracked repo never shows the shared status page as
// an untracked file. Missing entirely, it is created with just that line.
// Existing without the line, the line is appended (a missing trailing
// newline is added first, so it never runs into the prior content). An
// existing exact "flywheel.md" line is left alone and never duplicated. It
// reports whether this call changed the file.
func IgnoreMarkdown(dir string) (added bool, err error) {
	path := filepath.Join(dir, ".gitignore")
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte("flywheel.md\n"), 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", path, err)
		}
		return true, nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimRight(line, "\r") == "flywheel.md" {
			return false, nil
		}
	}
	out := b
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, []byte("flywheel.md\n")...)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// gitIgnores runs the read-only `git check-ignore -q <path>` in dir and
// reports whether git would ignore path. Any failure — not a git work tree,
// git missing, or the path simply not ignored — reports false.
func gitIgnores(dir, path string) bool {
	cmd := exec.Command("git", "check-ignore", "-q", path)
	cmd.Dir = dir
	if _, err := cmd.Output(); err != nil {
		return false
	}
	return true
}

// claudeSettings, claudeHooks, claudeHookGroup and claudeHookCmd mirror just
// enough of Claude Code's .claude/settings.json hook schema to describe the
// four Claude Code hooks InitHooks installs.
type claudeSettings struct {
	Hooks claudeHooks `json:"hooks"`
}

type claudeHooks struct {
	SessionStart []claudeHookGroup `json:"SessionStart"`
	PostToolUse  []claudeHookGroup `json:"PostToolUse"`
	SessionEnd   []claudeHookGroup `json:"SessionEnd"`
	Stop         []claudeHookGroup `json:"Stop"`
}

type claudeHookGroup struct {
	Matcher string          `json:"matcher,omitempty"`
	Hooks   []claudeHookCmd `json:"hooks"`
}

type claudeHookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// The four hook scripts below each read the hook payload Claude Code sends
// as one line of JSON on stdin and pull a field out with sed, so the hook
// depends on nothing beyond a POSIX shell and the flywheel binary on PATH —
// no jq, no Node. sessionCommandHook additionally checks the tool's command
// text starts with "flywheel" before logging it, since the PostToolUse
// matcher can only select by tool name (Bash), not by command text. The Stop
// hook allows the stop when stop_hook_active is true (no loop), runs
// `flywheel gate` with its report on stderr, and blocks (exit 2, which Claude
// Code shows to the agent) only on gate's exit 6 — never on a gate error.
// It points gate at $CLAUDE_PROJECT_DIR (the project Claude Code opened), not the
// hook's working directory, which can be a subdirectory with no ledger.
// OpenCode has no way to block ending a session, so gate is Claude-only for now.
const (
	claudeSessionStartHook   = `sh -c 'j=$(cat); s=$(printf "%s" "$j" | sed -n "s/.*\"session_id\":\"\([^\"]*\)\".*/\1/p"); flywheel log --kind session_start --session "$s"'`
	claudeSessionEndHook     = `sh -c 'j=$(cat); s=$(printf "%s" "$j" | sed -n "s/.*\"session_id\":\"\([^\"]*\)\".*/\1/p"); flywheel log --kind session_end --session "$s"'`
	claudeSessionCommandHook = `sh -c 'j=$(cat); c=$(printf "%s" "$j" | sed -n "s/.*\"command\":\"\([^\"]*\)\".*/\1/p"); case "$c" in flywheel*) s=$(printf "%s" "$j" | sed -n "s/.*\"session_id\":\"\([^\"]*\)\".*/\1/p"); flywheel log --kind session_command --session "$s" --note "$c";; esac'`
	claudeStopGateHook       = `sh -c 'j=$(cat); printf "%s" "$j" | grep -q "\"stop_hook_active\" *: *true" && exit 0; flywheel gate --dir "${CLAUDE_PROJECT_DIR:-.}" >&2; [ $? -eq 6 ] && exit 2; exit 0'`
)

// claudeSettingsPayload renders the .claude/settings.json bytes InitHooks
// writes: SessionStart and SessionEnd record the session boundary, a
// PostToolUse hook matched on the Bash tool records any flywheel command the
// session ran, and a Stop hook blocks ending the session while work is left unjudged.
func claudeSettingsPayload() ([]byte, error) {
	cfg := claudeSettings{Hooks: claudeHooks{
		SessionStart: []claudeHookGroup{{Hooks: []claudeHookCmd{{Type: "command", Command: claudeSessionStartHook}}}},
		PostToolUse:  []claudeHookGroup{{Matcher: "Bash", Hooks: []claudeHookCmd{{Type: "command", Command: claudeSessionCommandHook}}}},
		SessionEnd:   []claudeHookGroup{{Hooks: []claudeHookCmd{{Type: "command", Command: claudeSessionEndHook}}}},
		Stop:         []claudeHookGroup{{Hooks: []claudeHookCmd{{Type: "command", Command: claudeStopGateHook}}}},
	}}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// opencodeSessionPlugin is the .opencode/plugin/flywheel-session.mjs
// InitHooks writes: an OpenCode plugin with the same three hooks as
// claudeSettingsPayload, using node:child_process directly (no OpenCode
// helper, no jq) so the only runtime requirement is the flywheel binary on
// PATH.
const opencodeSessionPlugin = `// flywheel-session.mjs records session_start, session_command and
// session_end events to .flywheel/events.jsonl by invoking the flywheel
// binary on PATH. Written by "flywheel init --hooks"; a rerun never
// overwrites this file, so local edits are safe.
import { execFile } from "node:child_process"

function log(args) {
  return new Promise((resolve) => {
    execFile("flywheel", args, () => resolve())
  })
}

export const FlywheelSession = async () => {
  return {
    event: async ({ event }) => {
      if (event.type === "session.created") {
        await log(["log", "--kind", "session_start", "--session", event.properties.sessionID])
      } else if (event.type === "session.deleted" || event.type === "session.idle") {
        await log(["log", "--kind", "session_end", "--session", event.properties.sessionID])
      }
    },
    "tool.execute.after": async (input, output) => {
      const command = output && output.title
      if (typeof command !== "string" || !command.startsWith("flywheel")) return
      await log(["log", "--kind", "session_command", "--session", input.sessionID, "--note", command])
    },
  }
}
`

// InitHooks writes the two agent-hook files that record a session's
// lifecycle to the event log: .claude/settings.json (Claude Code hooks) and
// .opencode/plugin/flywheel-session.mjs (an OpenCode plugin). Both invoke
// `flywheel log --kind <kind> --session <id> [--note <command>]` — session_start
// on a session's first turn, session_command for a flywheel command the
// session runs, session_end when the session ends, and on Claude Code a fourth
// Stop hook blocks ending the session while work is left unjudged — resolving
// the flywheel binary from PATH and the session id from the platform's own
// hook environment. Like every InitSeeded piece, each file is created only when
// missing and never overwritten; a failure after one file is written rolls
// back exactly what this call created, so a retry starts clean.
func InitHooks(dir string) (string, []ScaffoldPiece, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %q: %w", dir, err)
	}
	settingsPath := filepath.Join(abs, ".claude", "settings.json")
	pluginPath := filepath.Join(abs, ".opencode", "plugin", "flywheel-session.mjs")

	settingsJSON, err := claudeSettingsPayload()
	if err != nil {
		return "", nil, fmt.Errorf("encode %s: %w", settingsPath, err)
	}

	var createdSettings, createdPlugin bool
	rollback := func() {
		if createdSettings {
			_ = os.Remove(settingsPath)
		}
		if createdPlugin {
			_ = os.Remove(pluginPath)
		}
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return "", nil, fmt.Errorf("create %s: %w", filepath.Dir(settingsPath), err)
	}
	if createdSettings, err = createIfMissing(settingsPath, settingsJSON); err != nil {
		rollback()
		return "", nil, fmt.Errorf("write %s: %w", settingsPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
		rollback()
		return "", nil, fmt.Errorf("create %s: %w", filepath.Dir(pluginPath), err)
	}
	if createdPlugin, err = createIfMissing(pluginPath, []byte(opencodeSessionPlugin)); err != nil {
		rollback()
		return "", nil, fmt.Errorf("write %s: %w", pluginPath, err)
	}

	return abs, []ScaffoldPiece{
		{Path: ".claude/settings.json", Added: createdSettings},
		{Path: ".opencode/plugin/flywheel-session.mjs", Added: createdPlugin},
	}, nil
}
