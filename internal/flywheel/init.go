package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// events.jsonl, config.json, .gitignore, .gitattributes, briefs/).
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
	path, _, err := InitSeeded(dir, force, "", "")
	return path, err
}

// InitSeeded is Init with optional model and variant seeding, and reports
// the status of every scaffold piece instead of just Init's path and error.
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
func InitSeeded(dir string, force bool, model, variant string) (string, []ScaffoldPiece, error) {
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

	if err := preflightMarkdown(mdPath); err != nil {
		return "", nil, err
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

	// rollback undoes this call's own footprint after an error: restore
	// flywheel.md's preexisting bytes if force reset it, remove pieces this
	// call created, and remove directories this call created (only if empty,
	// never recursive).
	rollback := func() {
		if mdAdded {
			if mdExisted {
				_ = os.WriteFile(mdPath, mdPrev, 0o644)
			} else {
				_ = os.Remove(mdPath)
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
	createdGitignore, err = createIfMissing(gitignorePath, []byte("runs/\n"))
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

	return abs, pieces, nil
}

// preflightMarkdown checks that mdPath is a usable destination for
// flywheel.md — absent, or an existing regular file — before InitSeeded
// touches anything. A directory or symlink there is refused unconditionally:
// a fresh write would go through it, and a force reset must not write
// through a symlink.
func preflightMarkdown(path string) error {
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
	return ignored
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
