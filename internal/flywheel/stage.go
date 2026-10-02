package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// StageMap is one `stage: <from> -> <to>` brief line (issue #781): flywheel
// validate copies every file under From to the same relative path under To
// before the gates, so a unit can stage files for a path its worker cannot
// write (.claude/, say). Both are repo-relative, forward slashes, exactly one
// trailing "/".
type StageMap struct {
	From, To string
}

// StagedFile is one file applyStage staged: its source and destination
// (repo-relative, forward slashes), the hex SHA-256 of its content, and
// whether the destination was written (false when it already held the bytes).
type StagedFile struct {
	From   string `json:"from"`
	To     string `json:"to"`
	SHA256 string `json:"sha256"`
	Copied bool   `json:"copied"`
}

// parseStage parses a stage: value. why is empty for a valid line and
// otherwise says what is wrong with it.
func parseStage(val string) (m StageMap, why string) {
	from, to, ok := strings.Cut(val, "->")
	if !ok {
		return StageMap{}, `missing "->"`
	}
	var sides [2]string
	for i, s := range []string{from, to} {
		s = strings.ReplaceAll(strings.TrimSpace(s), `\`, "/")
		switch {
		case s == "":
			return StageMap{}, "an empty side"
		case path.IsAbs(s) || filepath.IsAbs(s) || filepath.VolumeName(s) != "" || (len(s) > 1 && s[1] == ':'):
			return StageMap{}, fmt.Sprintf("%s is absolute", s)
		case slices.Contains(strings.Split(s, "/"), ".."):
			return StageMap{}, fmt.Sprintf(`%s has a ".." segment`, s)
		}
		sides[i] = strings.TrimSuffix(path.Clean(s), "/") + "/"
	}
	m = StageMap{From: sides[0], To: sides[1]}
	switch {
	case m.From == m.To:
		return StageMap{}, "from and to are the same path"
	case m.From == "./" || m.To == "./" || strings.HasPrefix(m.From, m.To) || strings.HasPrefix(m.To, m.From):
		return StageMap{}, "one side is inside the other"
	case claudeProtectedPath(strings.TrimSuffix(m.From, "/")):
		return StageMap{}, fmt.Sprintf("from %s is a path Claude Code protects; stage from a writable path", m.From)
	}
	return m, ""
}

// stageRefusal refuses a brief with an invalid stage: line (issue #781), nil
// when every line is valid.
func stageRefusal(h BriefHeader) *RuleRefusal {
	if len(h.stageInvalid) == 0 {
		return nil
	}
	_, why := parseStage(h.stageInvalid[0])
	return &RuleRefusal{Rule: "stage", Fix: fmt.Sprintf("brief line `stage: %s` is invalid (%s); write it as `stage: staging/claude/ -> .claude/`", h.stageInvalid[0], why)}
}

// applyStage copies, for each map, every regular file under wd/From to the
// same relative path under wd/To (issue #781). A missing From yields nothing;
// symlinks are skipped. A destination already holding the bytes is left
// alone (Copied false); any other is written atomically, a temp file in its
// directory renamed over it. The result is sorted by From.
func applyStage(wd string, maps []StageMap) ([]StagedFile, error) {
	var out []StagedFile
	for _, m := range maps {
		root := filepath.Join(wd, filepath.FromSlash(m.From))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root && errors.Is(err, fs.ErrNotExist) {
					return fs.SkipAll
				}
				return fmt.Errorf("stage walk %s: %w", p, err)
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return fmt.Errorf("stage %s: %w", p, err)
			}
			rel = filepath.ToSlash(rel)
			f, err := stageFile(wd, m.From+rel, m.To+rel)
			if err != nil {
				return err
			}
			out = append(out, f)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(out, func(a, b StagedFile) int { return strings.Compare(a.From, b.From) })
	return out, nil
}

// stageFile copies the repo-relative file from to to under wd unless to
// already holds the same bytes.
func stageFile(wd, from, to string) (StagedFile, error) {
	src := filepath.Join(wd, filepath.FromSlash(from))
	b, err := os.ReadFile(src)
	if err != nil {
		return StagedFile{}, fmt.Errorf("stage read %s: %w", from, err)
	}
	sum := sha256.Sum256(b)
	f := StagedFile{From: from, To: to, SHA256: hex.EncodeToString(sum[:])}
	dst := filepath.Join(wd, filepath.FromSlash(to))
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, b) {
		return f, nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return StagedFile{}, fmt.Errorf("stage stat %s: %w", from, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return StagedFile{}, fmt.Errorf("stage mkdir for %s: %w", to, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".stage-*")
	if err != nil {
		return StagedFile{}, fmt.Errorf("stage temp for %s: %w", to, err)
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), info.Mode().Perm())
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return StagedFile{}, fmt.Errorf("stage write %s: %w", to, err)
	}
	f.Copied = true
	return f, nil
}

// ownsUnder reports whether owns covers dir (a "/"-terminated path) or has a
// positive entry under it, the lint check for a stage: line's From.
func ownsUnder(owns []string, dir string) bool {
	if ownsContains(owns, dir) {
		return true
	}
	for _, o := range owns {
		if !strings.HasPrefix(o, "!") && strings.HasPrefix(filepath.ToSlash(o), dir) {
			return true
		}
	}
	return false
}

// stagedOwns is owns plus each staged destination whose source owns covers
// (issue #781): the owns check treats a destination as owned through its
// staged source. A destination whose source is not owned is not added.
func stagedOwns(owns []string, files []StagedFile) []string {
	out := slices.Clone(owns)
	for _, f := range files {
		if ownsContains(owns, f.From) && !slices.Contains(out, f.To) {
			out = append(out, f.To)
		}
	}
	return out
}

// recordedStaged is every file task's staged and unstaged events record,
// de-duplicated by To with the latest record winning (issue #781): a
// destination stays owned through its source after flywheel unstage removed
// the source, so the next validate pass does not see it outside owns.
func recordedStaged(events []Event, task string) []StagedFile {
	var out []StagedFile
	for _, e := range events {
		if e.Task != task || (e.Kind != "staged" && e.Kind != "unstaged") {
			continue
		}
		for _, f := range e.Staged {
			if i := slices.IndexFunc(out, func(g StagedFile) bool { return g.To == f.To }); i >= 0 {
				out[i] = f
			} else {
				out = append(out, f)
			}
		}
	}
	return out
}
