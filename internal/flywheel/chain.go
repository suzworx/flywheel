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
	"time"
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

// lastCompleteLineOf returns the last complete, non-blank line of the file
// at path (without its newline) — the line lastLineHash hashes — and found
// false when the file is missing or has none.
func lastCompleteLineOf(path string) (line []byte, found bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat %s: %w", path, err)
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
			return nil, false, fmt.Errorf("read %s: %w", path, err)
		}
		tail = append(buf, tail...)
		if cand, foundLine, ok := lastCompleteLine(tail, off == 0); ok {
			return cand, foundLine, nil
		}
	}
	return nil, false, nil
}

// lastLineHash returns lineHash of the last COMPLETE, non-blank line of the
// log at path ("" when the file is missing or has no such line) — exactly the
// lines VerifyLogChain hashes, so a blank or unterminated tail never becomes
// a predecessor (#299 review). It reads backwards in
// 64 KiB chunks from the end, so a large log is never read whole. An
// unterminated tail (a record still being appended) is not a complete line
// and is skipped; the complete line before it is used.
func lastLineHash(path string) (string, error) {
	line, found, err := lastCompleteLineOf(path)
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	return lineHash(line), nil
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
	File         string `json:"file,omitempty"`
	Files        int    `json:"files,omitempty"`
	BreakReason  string `json:"break_reason,omitempty"`
}

// OK reports whether the chain is intact.
func (c LogChain) OK() bool { return c.BreakLine == 0 }

// VerifyLogChain reads the log (legacy or sharded) and checks that every
// line's prev equals the lineHash of some earlier line in the same file; it
// stops at the first line whose prev matches none. Lines without prev are
// counted but not checked. In sharded layout, it verifies the seal matches
// the legacy file's current state.
func VerifyLogChain(dir string) (LogChain, error) {
	sharded, err := ShardedLayout(dir)
	if err != nil {
		return LogChain{}, err
	}

	if sharded {
		return verifyShardedChain(dir)
	}

	return verifyLegacyChain(dir)
}

// verifyLegacyChain checks the legacy events.jsonl chain.
func verifyLegacyChain(dir string) (LogChain, error) {
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

// verifyShardedChain checks the sharded log chain: legacy file (if present),
// every shard in logFilesOf order, and the seal.
func verifyShardedChain(dir string) (LogChain, error) {
	files, err := logFilesOf(dir)
	if err != nil {
		return LogChain{}, err
	}

	var result LogChain
	result.Files = len(files)

	// Verify each file
	for _, lf := range files {
		var chainResult LogChain
		var err error

		// Legacy file uses legacy rules; shards use strict prev rules
		if lf.Rel == "events.jsonl" {
			chainResult, err = verifyFileLegacyChain(lf.Path, lf.Rel)
		} else {
			chainResult, err = verifyFileChain(lf.Path, lf.Rel)
		}

		if err != nil {
			return LogChain{}, err
		}
		result.Lines += chainResult.Lines
		result.Chained += chainResult.Chained
		if !chainResult.OK() {
			result.BreakLine = chainResult.BreakLine
			result.BreakPrev = chainResult.BreakPrev
			result.BreakReason = chainResult.BreakReason
			result.File = chainResult.File
			return result, nil
		}
	}

	// Verify the seal exists and matches the legacy file's current state
	legacyPath := filepath.Join(dir, ".flywheel", legacyLogName)
	legacyHash, err := lastLineHash(legacyPath)
	if err != nil {
		return LogChain{}, err
	}

	// Get the last seal from @floor.jsonl
	floorPath := filepath.Join(dir, ".flywheel", shardDirName, floorShard)
	lastSeal, err := lastSealEvent(floorPath)
	if err != nil {
		return LogChain{}, err
	}

	if lastSeal == nil {
		// Sharded layout requires a seal unconditionally. BreakLine is legacy line count + 1.
		legacyData, _ := os.ReadFile(legacyPath)
		legacyLines := countCompleteLines(legacyData)
		result.BreakReason = "sharded layout has no seal of events.jsonl"
		result.File = "events.jsonl"
		result.BreakLine = legacyLines + 1
		return result, nil
	}

	// Verify seal matches
	if lastSeal.SHA256 != legacyHash {
		// Check if it's an appended case: sealed hash exists in current legacy file
		legacyData, err := os.ReadFile(legacyPath)
		if err != nil && !os.IsNotExist(err) {
			return LogChain{}, fmt.Errorf("read %s: %w", legacyPath, err)
		}

		// Count how many lines were appended after the sealed hash
		appendedLines := countAppendedLines(legacyData, lastSeal.SHA256)
		if appendedLines > 0 {
			result.BreakReason = fmt.Sprintf("events.jsonl has %d line(s) appended after it was sealed (a binary without shard support wrote them); run flywheel log --shard", appendedLines)
		} else {
			// Sealed hash doesn't match any line in current file
			legacyLines := countCompleteLines(legacyData)
			if legacyLines == 0 && lastSeal.SHA256 == "" {
				// Empty file and empty seal is OK
				return result, nil
			}
			if legacyLines > 0 && lastSeal.SHA256 == "" {
				// Sealed as empty ("") but file now has lines
				result.BreakReason = fmt.Sprintf("events.jsonl has %d line(s) appended after it was sealed (a binary without shard support wrote them); run flywheel log --shard", legacyLines)
			} else {
				result.BreakReason = "events.jsonl was edited or truncated after it was sealed"
			}
		}
		result.File = "events.jsonl"
		legacyLines := countCompleteLines(legacyData)
		result.BreakLine = legacyLines + 1
		return result, nil
	}

	return result, nil
}

// verifyFileLegacyChain verifies one legacy file's chain using legacy rules
// (lines without prev are skipped, not required).
func verifyFileLegacyChain(path, rel string) (LogChain, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LogChain{File: rel}, nil
		}
		return LogChain{}, fmt.Errorf("read %s: %w", path, err)
	}

	// Drop unterminated tail
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lastNL := strings.LastIndexByte(string(data), '\n')
		if lastNL == -1 {
			return LogChain{File: rel}, nil
		}
		data = data[:lastNL+1]
	}

	lines := strings.Split(string(data), "\n")
	seen := make(map[string]bool)
	var result LogChain
	result.File = rel

	for i, rawLine := range lines {
		lineNum := i + 1
		if rawLine == "" {
			continue
		}

		result.Lines++

		// Decode prev field only
		var record struct {
			Prev string `json:"prev"`
		}
		if err := json.Unmarshal([]byte(rawLine), &record); err != nil {
			return LogChain{}, fmt.Errorf("%s line %d: %w", rel, lineNum, err)
		}

		// Legacy rules: lines without prev are counted but not checked
		if record.Prev != "" {
			if result.FirstChained == 0 {
				result.FirstChained = lineNum
			}
			result.Chained++

			// Check that prev matches some earlier line
			if !seen[record.Prev] {
				result.BreakLine = lineNum
				result.BreakPrev = record.Prev
				result.BreakReason = "prev matches no earlier line"
				result.File = rel
				return result, nil
			}
		}

		// Add this line's hash to seen
		h := lineHash([]byte(rawLine))
		seen[h] = true
	}

	return result, nil
}

// verifyFileChain verifies one file's chain, returning a result with File set.
func verifyFileChain(path, rel string) (LogChain, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LogChain{File: rel}, nil
		}
		return LogChain{}, fmt.Errorf("read %s: %w", path, err)
	}

	// Drop unterminated tail
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lastNL := strings.LastIndexByte(string(data), '\n')
		if lastNL == -1 {
			return LogChain{File: rel}, nil
		}
		data = data[:lastNL+1]
	}

	lines := strings.Split(string(data), "\n")
	seen := make(map[string]bool)
	first := true // only the first line may chain to the genesis hash
	var result LogChain
	result.File = rel

	for i, rawLine := range lines {
		lineNum := i + 1
		if rawLine == "" {
			continue
		}

		result.Lines++

		// Decode prev field only
		var record struct {
			Prev string `json:"prev"`
		}
		if err := json.Unmarshal([]byte(rawLine), &record); err != nil {
			return LogChain{}, fmt.Errorf("%s line %d: %w", rel, lineNum, err)
		}

		// In sharded files, every line must carry prev
		if record.Prev == "" {
			result.BreakLine = lineNum
			result.BreakReason = "line has no prev"
			result.File = rel
			return result, nil
		}

		result.Chained++

		// Check that prev matches some earlier line or genesis
		if first {
			// The first line of a shard chains to the genesis hash and to
			// nothing else.
			if record.Prev != shardGenesis {
				result.BreakLine = lineNum
				result.BreakPrev = record.Prev
				result.BreakReason = "first line does not chain to the shard genesis"
				result.File = rel
				return result, nil
			}
			first = false
		} else if record.Prev == shardGenesis || !seen[record.Prev] {
			result.BreakLine = lineNum
			result.BreakPrev = record.Prev
			result.BreakReason = "prev matches no earlier line"
			result.File = rel
			return result, nil
		}

		// Add this line's hash to seen
		h := lineHash([]byte(rawLine))
		seen[h] = true
	}

	return result, nil
}

// countAppendedLines counts how many complete lines in data come after the line
// with hash sealedHash. Returns 0 if sealedHash is not found or is empty.
func countAppendedLines(data []byte, sealedHash string) int {
	if sealedHash == "" || len(data) == 0 {
		return 0
	}

	// Drop unterminated tail
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lastNL := bytes.LastIndexByte(data, '\n')
		if lastNL == -1 {
			return 0
		}
		data = data[:lastNL+1]
	}

	lines := bytes.Split(data, []byte("\n"))
	foundIdx := -1

	for i, line := range lines {
		line := bytes.TrimSuffix(line, []byte("\r"))
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if lineHash(line) == sealedHash {
			foundIdx = i
			break
		}
	}

	if foundIdx < 0 {
		return 0
	}

	// Count complete lines after foundIdx
	count := 0
	for i := foundIdx + 1; i < len(lines); i++ {
		line := bytes.TrimSuffix(lines[i], []byte("\r"))
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	return count
}

// eventsLockTimings are the append lock's timings: it is held for one read of
// the log's last line and one write, so it retries often and waits long —
// an append must never fail because other appends were busy (TestAppendConcurrent
// runs fifty at once) — and a lock left by a crashed writer goes stale fast.
func eventsLockTimings() repoLockTimings {
	return repoLockTimings{
		staleAfter: 10 * time.Second,
		wait:       30 * time.Second,
		retry:      5 * time.Millisecond,
		heartbeat:  2 * time.Second,
	}
}
