package term

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadKeyArrows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seq  string
		want KeyKind
	}{
		{"ESC[A up", "\x1b[A", KeyUp},
		{"ESC[B down", "\x1b[B", KeyDown},
		{"ESC[C right", "\x1b[C", KeyRight},
		{"ESC[D left", "\x1b[D", KeyLeft},
		{"ESC OA up alt", "\x1bOA", KeyUp},
		{"ESC OB down alt", "\x1bOB", KeyDown},
		{"ESC OC right alt", "\x1bOC", KeyRight},
		{"ESC OD left alt", "\x1bOD", KeyLeft},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tt.seq))
			k, err := ReadKey(r)
			if err != nil {
				t.Fatalf("ReadKey error: %v", err)
			}
			if k.Kind != tt.want {
				t.Errorf("got %v, want %v", k.Kind, tt.want)
			}
		})
	}
}

func TestReadKeyNavigation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seq  string
		want KeyKind
	}{
		{"ESC[5~ PgUp", "\x1b[5~", KeyPgUp},
		{"ESC[6~ PgDn", "\x1b[6~", KeyPgDn},
		{"ESC[H Home", "\x1b[H", KeyHome},
		{"ESC[1~ Home alt", "\x1b[1~", KeyHome},
		{"ESC[7~ Home alt2", "\x1b[7~", KeyHome},
		{"ESC[F End", "\x1b[F", KeyEnd},
		{"ESC[4~ End alt", "\x1b[4~", KeyEnd},
		{"ESC[8~ End alt2", "\x1b[8~", KeyEnd},
		{"ESC[3~ Delete", "\x1b[3~", KeyDelete},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tt.seq))
			k, err := ReadKey(r)
			if err != nil {
				t.Fatalf("ReadKey error: %v", err)
			}
			if k.Kind != tt.want {
				t.Errorf("got %v, want %v", k.Kind, tt.want)
			}
		})
	}
}

func TestReadKeyControl(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seq  string
		want KeyKind
	}{
		{"CR Enter", "\r", KeyEnter},
		{"LF Enter", "\n", KeyEnter},
		{"127 Backspace", "\x7f", KeyBackspace},
		{"8 Backspace", "\x08", KeyBackspace},
		{"9 Tab", "\x09", KeyTab},
		{"3 CtrlC", "\x03", KeyCtrlC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tt.seq))
			k, err := ReadKey(r)
			if err != nil {
				t.Fatalf("ReadKey error: %v", err)
			}
			if k.Kind != tt.want {
				t.Errorf("got %v, want %v", k.Kind, tt.want)
			}
		})
	}
}

func TestReadKeyLoneEsc(t *testing.T) {
	t.Parallel()
	r := bufio.NewReader(strings.NewReader("\x1b"))
	k, err := ReadKey(r)
	if err != nil {
		t.Fatalf("ReadKey error: %v", err)
	}
	if k.Kind != KeyEsc {
		t.Errorf("got %v, want KeyEsc", k.Kind)
	}

	// Next read should return io.EOF
	_, err = ReadKey(r)
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestReadKeyRunes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seq  string
		want rune
	}{
		{"j", "j", 'j'},
		{"é", "é", 'é'},
		{":", ":", ':'},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tt.seq))
			k, err := ReadKey(r)
			if err != nil {
				t.Fatalf("ReadKey error: %v", err)
			}
			if k.Kind != KeyRune {
				t.Errorf("got %v, want KeyRune", k.Kind)
			}
			if k.Rune != tt.want {
				t.Errorf("got rune %q, want %q", k.Rune, tt.want)
			}
		})
	}

	// Test sequence of keys in one reader
	r := bufio.NewReader(strings.NewReader("jk\x1b[A"))
	keys := []struct {
		kind KeyKind
		rune rune
	}{
		{KeyRune, 'j'},
		{KeyRune, 'k'},
		{KeyUp, 0},
	}
	for i, want := range keys {
		k, err := ReadKey(r)
		if err != nil {
			t.Fatalf("ReadKey %d error: %v", i, err)
		}
		if k.Kind != want.kind {
			t.Errorf("key %d: got %v, want %v", i, k.Kind, want.kind)
		}
		if want.rune != 0 && k.Rune != want.rune {
			t.Errorf("key %d: got rune %q, want %q", i, k.Rune, want.rune)
		}
	}
}

func TestIsTerminalFile(t *testing.T) {
	t.Parallel()
	// Test with a regular temp file (not a terminal)
	f := t.TempDir()
	tmpFile := f + "/test.txt"
	file, err := os.Create(tmpFile)
	if err != nil {
		t.Fatalf("Create temp file: %v", err)
	}
	defer file.Close()

	if IsTerminal(file) {
		t.Errorf("IsTerminal on regular file should be false")
	}

	// Test MakeRaw on non-terminal file
	_, err = MakeRaw(file)
	if err == nil {
		t.Errorf("MakeRaw on non-terminal file should error")
	}

	// Test Size on non-terminal file
	_, _, err = Size(file)
	if err == nil {
		t.Errorf("Size on non-terminal file should error")
	}
}

// TestReadKeyModifiedAndAltSequences checks that a modified arrow's
// parameters are consumed (Ctrl-Up is Up, nothing leaks as runes) and that an
// Alt-modified key is that key with Alt set and does not swallow the key
// after it.
func TestReadKeyModifiedAndAltSequences(t *testing.T) {
	t.Parallel()
	r := bufio.NewReader(strings.NewReader("\x1b[1;5Aj\x1bxk"))
	want := []Key{{Kind: KeyUp}, {Kind: KeyRune, Rune: 'j'}, {Kind: KeyRune, Rune: 'x', Alt: true}, {Kind: KeyRune, Rune: 'k'}}
	for i, w := range want {
		got, err := ReadKey(r)
		if err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
		if got != w {
			t.Errorf("key %d = %+v, want %+v", i, got, w)
		}
	}
}

// TestReadKeyAltKeys checks the Alt contract: ESC then a key in the same
// burst is that key with Alt set; ESC ESC is Alt-Esc; Alt-Enter is Enter.
func TestReadKeyAltKeys(t *testing.T) {
	t.Parallel()
	r := bufio.NewReader(strings.NewReader("\x1b\x1b\x1b\r\x1bé"))
	want := []Key{{Kind: KeyEsc, Alt: true}, {Kind: KeyEnter, Alt: true}, {Kind: KeyRune, Rune: 'é', Alt: true}}
	for i, w := range want {
		got, err := ReadKey(r)
		if err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
		if got != w {
			t.Errorf("key %d = %+v, want %+v", i, got, w)
		}
	}
}

// TestReaderFragmentedSequence checks that a Reader waits for the rest of an
// escape sequence that arrives in pieces (#341 review): ESC, then "[A" a
// moment later, is Up, not Esc followed by '[' and 'A'.
func TestReaderFragmentedSequence(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	defer pw.Close()
	r := NewReader(pr, 2*time.Second)
	go func() {
		pw.Write([]byte("\x1b"))
		time.Sleep(20 * time.Millisecond)
		pw.Write([]byte("["))
		time.Sleep(20 * time.Millisecond)
		pw.Write([]byte("6~j"))
	}()
	for i, w := range []Key{{Kind: KeyPgDn}, {Kind: KeyRune, Rune: 'j'}} {
		got, err := r.ReadKey()
		if err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
		if got != w {
			t.Errorf("key %d = %+v, want %+v", i, got, w)
		}
	}
}

// TestReaderLoneEscAfterDelay checks that a Reader reports a lone ESC once
// the delay passes with nothing more, then keeps decoding.
func TestReaderLoneEscAfterDelay(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	defer pw.Close()
	r := NewReader(pr, 10*time.Millisecond)
	go pw.Write([]byte("\x1b"))
	got, err := r.ReadKey()
	if err != nil || got != (Key{Kind: KeyEsc}) {
		t.Fatalf("ReadKey() = %+v, %v, want KeyEsc", got, err)
	}
	go pw.Write([]byte("q"))
	got, err = r.ReadKey()
	if err != nil || got != (Key{Kind: KeyRune, Rune: 'q'}) {
		t.Fatalf("ReadKey() = %+v, %v, want q", got, err)
	}
}

// TestReaderEOF checks that a Reader decodes what it read, multi-byte runes
// included, and then returns the reader's error.
func TestReaderEOF(t *testing.T) {
	t.Parallel()
	r := NewReader(strings.NewReader("é\x1b[Bz"), 0)
	for i, w := range []Key{{Kind: KeyRune, Rune: 'é'}, {Kind: KeyDown}, {Kind: KeyRune, Rune: 'z'}} {
		got, err := r.ReadKey()
		if err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
		if got != w {
			t.Errorf("key %d = %+v, want %+v", i, got, w)
		}
	}
	if _, err := r.ReadKey(); err != io.EOF {
		t.Errorf("ReadKey() at end: err = %v, want io.EOF", err)
	}
}
