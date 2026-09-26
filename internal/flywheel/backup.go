package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupResult is what Backup copied and what the copy's chain check found.
type BackupResult struct {
	Dest             string   `json:"dest"`
	Files            int      `json:"files"`
	Bytes            int64    `json:"bytes"`
	Skipped          []string `json:"skipped,omitempty"`
	PartialTailBytes int      `json:"partial_tail_bytes"`
	ChainOK          bool     `json:"chain_ok"`
	ChainLines       int      `json:"chain_lines"`
	ChainBreak       string   `json:"chain_break,omitempty"`
}

// BackupFile is one manifest entry: a copied file's path, size and sha256.
type BackupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type backupChain struct {
	OK    bool   `json:"ok"`
	Lines int    `json:"lines"`
	Break string `json:"break,omitempty"`
}

type backupManifest struct {
	Created          string       `json:"created"`
	Source           string       `json:"source"`
	Files            []BackupFile `json:"files"`
	Skipped          []string     `json:"skipped"`
	PartialTailBytes int          `json:"partial_tail_bytes"`
	Chain            backupChain  `json:"chain"`
}

// Backup copies dir's .flywheel/ to dest/.flywheel/ as one point-in-time,
// verified copy (issue #464): live process state (top-level worktrees/ and
// locks/, *.tmp files) is left out, symlinks and junctions are skipped and
// listed, and append-only logs are copied by complete lines only. The copy is
// built in a sibling temp directory, given a manifest (flywheel-backup.json)
// and chain-checked, then renamed onto dest; on error dest is left as it was.
// A broken chain is recorded, not an error: a copy of it is still worth keeping.
func Backup(dir, dest string, now time.Time) (BackupResult, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return BackupResult{}, fmt.Errorf("resolve %s: %w", dir, err)
	}
	src := filepath.Join(absDir, ".flywheel")
	realSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		return BackupResult{}, fmt.Errorf("no ledger at %s: %w", src, err)
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return BackupResult{}, fmt.Errorf("resolve %s: %w", dest, err)
	}
	realDest, err := resolveExistingPrefix(absDest)
	if err != nil {
		return BackupResult{}, fmt.Errorf("resolve %s: %w", dest, err)
	}
	if rel, err := filepath.Rel(realSrc, realDest); err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
		return BackupResult{}, fmt.Errorf("backup destination %s is inside the ledger %s", absDest, src)
	}
	destExists := false
	if fi, err := os.Lstat(absDest); err == nil {
		if !fi.IsDir() {
			return BackupResult{}, fmt.Errorf("backup destination %s is not a directory", absDest)
		}
		entries, err := os.ReadDir(absDest)
		if err != nil {
			return BackupResult{}, fmt.Errorf("read %s: %w", absDest, err)
		}
		if len(entries) > 0 {
			return BackupResult{}, fmt.Errorf("backup destination %s is not empty", absDest)
		}
		destExists = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return BackupResult{}, fmt.Errorf("stat %s: %w", absDest, err)
	}
	parent := filepath.Dir(absDest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return BackupResult{}, fmt.Errorf("create %s: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, ".flywheel-backup-*")
	if err != nil {
		return BackupResult{}, fmt.Errorf("create temp dir in %s: %w", parent, err)
	}
	res, err := buildBackup(realSrc, absDir, tmp, now)
	if err == nil && destExists {
		if err = os.Remove(absDest); err != nil {
			err = fmt.Errorf("replace empty %s: %w", absDest, err)
		}
	}
	if err == nil {
		if err = os.Rename(tmp, absDest); err != nil {
			err = fmt.Errorf("move backup to %s: %w", absDest, err)
			if destExists {
				_ = os.Mkdir(absDest, 0o755)
			}
		}
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
		return BackupResult{}, err
	}
	res.Dest = absDest
	return res, nil
}

// resolveExistingPrefix evaluates symlinks in p's nearest existing ancestor
// and rejoins the not-yet-existing rest, so a path can be compared by location.
func resolveExistingPrefix(p string) (string, error) {
	rest := ""
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		up := filepath.Dir(p)
		if up == p {
			return filepath.Join(p, rest), nil
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = up
	}
}

// buildBackup copies src (a .flywheel directory) into root/.flywheel, writes
// root/flywheel-backup.json and chain-checks the copy.
func buildBackup(src, source, root string, now time.Time) (BackupResult, error) {
	var res BackupResult
	m := backupManifest{Created: now.UTC().Format(time.RFC3339), Source: source, Files: []BackupFile{}, Skipped: []string{}}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", path, err)
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if t := info.Mode().Type(); d.Type()&^fs.ModeDir != 0 || (t != 0 && t != fs.ModeDir) {
			m.Skipped = append(m.Skipped, ".flywheel/"+rel)
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		out := filepath.Join(root, ".flywheel", filepath.FromSlash(rel))
		if d.IsDir() {
			if rel == "worktrees" || rel == "locks" {
				return fs.SkipDir
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", out, err)
			}
			return nil
		}
		if strings.HasSuffix(rel, ".tmp") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if rel == "events.jsonl" || strings.HasPrefix(rel, "events/") {
			keep := bytes.LastIndexByte(data, '\n') + 1
			m.PartialTailBytes += len(data) - keep
			data = data[:keep]
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		sum := sha256.Sum256(data)
		m.Files = append(m.Files, BackupFile{Path: ".flywheel/" + rel, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		res.Bytes += int64(len(data))
		return nil
	})
	if err != nil {
		return BackupResult{}, err
	}
	chain, err := VerifyLogChain(root)
	if err != nil {
		return BackupResult{}, fmt.Errorf("verify the copy's chain: %w", err)
	}
	m.Chain = backupChain{OK: chain.OK(), Lines: chain.Lines}
	if !chain.OK() {
		file := chain.File
		if file == "" {
			file = "events.jsonl"
		}
		m.Chain.Break = strings.TrimSpace(fmt.Sprintf("%s:%d %s", file, chain.BreakLine, chain.BreakReason))
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return BackupResult{}, fmt.Errorf("encode manifest: %w", err)
	}
	manifest := filepath.Join(root, "flywheel-backup.json")
	if err := os.WriteFile(manifest, append(b, '\n'), 0o644); err != nil {
		return BackupResult{}, fmt.Errorf("write %s: %w", manifest, err)
	}
	res.Files = len(m.Files)
	if len(m.Skipped) > 0 {
		res.Skipped = m.Skipped
	}
	res.PartialTailBytes = m.PartialTailBytes
	res.ChainOK, res.ChainLines, res.ChainBreak = m.Chain.OK, m.Chain.Lines, m.Chain.Break
	return res, nil
}
