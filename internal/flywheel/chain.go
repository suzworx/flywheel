package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// lineHash is the chain hash of one log line: SHA-256, hex, of its bytes
// without the trailing newline and without a trailing carriage return.
func lineHash(line []byte) string {
	// Remove trailing carriage return if present (Windows CRLF → LF)
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	h := sha256.Sum256(line)
	return hex.EncodeToString(h[:])
}

// lastLineHash returns lineHash of the last COMPLETE, non-blank line of the
// log at path ("" when the file is missing or has no such line) — exactly the
// lines VerifyLogChain hashes, so a blank or unterminated tail never becomes
// a predecessor (#299 review). It reads backwards in
// 64 KiB chunks from the end, so a large log is never read whole. An
// unterminated tail (a record still being appended) is not a complete line
// and is skipped; the complete line before it is used.
func lastLineHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	// Walk backwards in chunks, prepending each chunk to tail, until tail
	// holds the last complete line: the bytes between the last two newlines
	// once any unterminated tail is dropped.
	const chunk = 64 * 1024
	var tail []byte
	for off := info.Size(); off > 0; {
		n := int64(chunk)
		if off < n {
			n = off
		}
		off -= n
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, off); err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		tail = append(buf, tail...)
		if line, found, ok := lastCompleteLine(tail, off == 0); ok {
			if !found {
				return "", nil
			}
			return lineHash(line), nil
		}
	}
	return "", nil
}

// lastCompleteLine returns the last newline-terminated, non-blank line in tail
// (without its newline) — the same lines VerifyLogChain hashes — dropping an
// unterminated tail and skipping blank lines. found is false when tail holds
// no such line; ok is false while tail does not yet reach back far enough to
// be sure and more of the file is left to read (atStart false).
func lastCompleteLine(tail []byte, atStart bool) (line []byte, found, ok bool) {
	end := bytes.LastIndexByte(tail, '\n')
	for end >= 0 {
		start := bytes.LastIndexByte(tail[:end], '\n')
		if start < 0 && !atStart {
			return nil, false, false
		}
		cand := tail[start+1 : end]
		if len(bytes.TrimSpace(cand)) > 0 {
			return cand, true, true
		}
		end = start
	}
	return nil, false, atStart
}

// LogChain is the result of checking the log's hash chain (issue #57).
type LogChain struct {
	Lines        int    `json:"lines"`         // complete, non-empty lines
	Chained      int    `json:"chained"`       // lines carrying prev
	FirstChained int    `json:"first_chained"` // 1-based line of the first chained record, 0 if none
	BreakLine    int    `json:"break_line"`    // 1-based line whose prev matches no earlier line; 0 = intact
	BreakPrev    string `json:"break_prev,omitempty"`
}

// OK reports whether the chain is intact.
func (c LogChain) OK() bool { return c.BreakLine == 0 }

// VerifyLogChain reads .flywheel/events.jsonl (complete lines only) and checks
// that every line's prev equals the lineHash of some earlier line; it stops at
// the first line whose prev matches none. Lines without prev (written before
// the chain existed) are counted but not checked.
func VerifyLogChain(dir string) (LogChain, error) {
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LogChain{}, nil
		}
		return LogChain{}, fmt.Errorf("read %s: %w", path, err)
	}

	// Drop unterminated tail
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lastNL := strings.LastIndexByte(string(data), '\n')
		if lastNL == -1 {
			// No complete line at all
			return LogChain{}, nil
		}
		data = data[:lastNL+1]
	}

	lines := strings.Split(string(data), "\n")
	seen := make(map[string]bool)
	var result LogChain

	for i, rawLine := range lines {
		lineNum := i + 1
		if rawLine == "" {
			continue
		}

		result.Lines++
		result.BreakLine = 0 // reset for each valid line

		// Decode prev field only
		var record struct {
			Prev string `json:"prev"`
		}
		if err := json.Unmarshal([]byte(rawLine), &record); err != nil {
			return LogChain{}, fmt.Errorf("line %d: %w", lineNum, err)
		}

		if record.Prev != "" {
			if result.FirstChained == 0 {
				result.FirstChained = lineNum
			}
			result.Chained++

			// Check that prev matches some earlier line
			if !seen[record.Prev] {
				result.BreakLine = lineNum
				result.BreakPrev = record.Prev
				return result, nil
			}
		}

		// Add this line's hash to seen
		h := lineHash([]byte(rawLine))
		seen[h] = true
	}

	return result, nil
}
