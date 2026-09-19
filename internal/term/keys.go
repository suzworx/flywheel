// Package term is flywheel's minimal terminal layer (issue #336): raw mode,
// key decoding and window size, standard library only.
package term

import (
	"bufio"
	"io"
	"strings"
	"time"
)

// KeyKind names a key.
type KeyKind int

const (
	KeyRune KeyKind = iota // a printable character; Key.Rune holds it
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyEnter
	KeyEsc
	KeyBackspace
	KeyTab
	KeyPgUp
	KeyPgDn
	KeyHome
	KeyEnd
	KeyDelete
	KeyCtrlC
)

// Key is one decoded key press.
type Key struct {
	Kind KeyKind
	Rune rune // for KeyRune
	// Alt marks a key that arrived prefixed by ESC in the same burst (Alt-x
	// on most terminals): Kind and Rune are the key itself, never KeyEsc.
	Alt bool
}

// byteSource is what the decoder reads from: ReadByte blocks for the next
// byte; More reports whether another byte is available now or shortly (the
// rest of an escape sequence), deciding between a lone ESC and a sequence.
type byteSource interface {
	ReadByte() (byte, error)
	UnreadByte() error
	More() bool
}

// bufferedSource decides with what the bufio.Reader already holds.
type bufferedSource struct{ *bufio.Reader }

func (s bufferedSource) More() bool { return s.Buffered() > 0 }

// ReadKey reads one key from r. Escape sequences: ESC [ A/B/C/D (arrows),
// ESC O A/B/C/D (the same), ESC [ H / ESC [ F / ESC [ 1~ / ESC [ 7~ (Home),
// ESC [ 4~ / ESC [ 8~ (End), ESC [ 5~ (PgUp), ESC [ 6~ (PgDn), ESC [ 3~
// (Delete). A lone ESC — nothing buffered after it — is KeyEsc; an unknown
// sequence is consumed and reported as KeyEsc. 13 or 10 is KeyEnter, 127 or 8
// KeyBackspace, 9 KeyTab, 3 KeyCtrlC; any other byte starts a UTF-8 rune
// (KeyRune). ESC followed by any other key in the same burst is that key
// with Alt set. io errors are returned as is. A terminal delivering an
// escape sequence in pieces (a slow link) needs a Reader, which waits a
// moment for the rest; ReadKey only sees what r already buffered.
func ReadKey(r *bufio.Reader) (Key, error) {
	return readKey(bufferedSource{r})
}

func readKey(r byteSource) (Key, error) {
	b, err := r.ReadByte()
	if err != nil {
		return Key{}, err
	}

	switch b {
	case 13, 10: // CR, LF
		return Key{Kind: KeyEnter}, nil
	case 127, 8: // DEL, BS
		return Key{Kind: KeyBackspace}, nil
	case 9: // TAB
		return Key{Kind: KeyTab}, nil
	case 3: // Ctrl-C
		return Key{Kind: KeyCtrlC}, nil
	case 27: // ESC
		if !r.More() {
			return Key{Kind: KeyEsc}, nil
		}
		return readEscapeSeq(r)
	default:
		return readRune(r, b)
	}
}

// readRune decodes the UTF-8 rune whose first byte is b.
func readRune(r byteSource, b byte) (Key, error) {
	n := 1
	switch {
	case b >= 0xf0:
		n = 4
	case b >= 0xe0:
		n = 3
	case b >= 0xc0:
		n = 2
	}
	buf := []byte{b}
	for len(buf) < n {
		c, err := r.ReadByte()
		if err != nil {
			return Key{}, err
		}
		buf = append(buf, c)
	}
	return Key{Kind: KeyRune, Rune: []rune(string(buf))[0]}, nil
}

// readEscapeSeq decodes what follows an ESC that arrived with more bytes:
// a CSI sequence (ESC [), an SS3 sequence (ESC O), or anything else — an
// Alt-modified key such as ESC x — which is that key with Alt set (ESC ESC
// is Alt-Esc; Alt-[ and Alt-O read as the start of a sequence).
func readEscapeSeq(r byteSource) (Key, error) {
	b, err := r.ReadByte()
	if err != nil {
		return Key{}, err
	}
	switch b {
	case '[':
		return readCSISeq(r)
	case 'O':
		f, err := r.ReadByte()
		if err != nil {
			return Key{}, err
		}
		return finalKey(f, ""), nil
	case 27:
		return Key{Kind: KeyEsc, Alt: true}, nil
	}
	if err := r.UnreadByte(); err != nil {
		return Key{}, err
	}
	k, err := readKey(r)
	k.Alt = true
	return k, err
}

// readCSISeq reads a CSI sequence up to and including its final byte
// (0x40–0x7E), so parameters and modifiers (ESC [ 1 ; 5 A for Ctrl-Up) are
// always consumed, and maps it: A/B/C/D/H/F by the final byte, "~" by the
// first parameter. Anything unknown is KeyEsc.
func readCSISeq(r byteSource) (Key, error) {
	var params []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return Key{}, err
		}
		if b >= 0x40 && b <= 0x7e {
			return finalKey(b, string(params)), nil
		}
		params = append(params, b)
	}
}

// finalKey maps a CSI/SS3 final byte, with its parameter string, to a key.
func finalKey(final byte, params string) Key {
	switch final {
	case 'A':
		return Key{Kind: KeyUp}
	case 'B':
		return Key{Kind: KeyDown}
	case 'C':
		return Key{Kind: KeyRight}
	case 'D':
		return Key{Kind: KeyLeft}
	case 'H':
		return Key{Kind: KeyHome}
	case 'F':
		return Key{Kind: KeyEnd}
	case '~':
		first, _, _ := strings.Cut(params, ";")
		switch first {
		case "1", "7":
			return Key{Kind: KeyHome}
		case "4", "8":
			return Key{Kind: KeyEnd}
		case "5":
			return Key{Kind: KeyPgUp}
		case "6":
			return Key{Kind: KeyPgDn}
		case "3":
			return Key{Kind: KeyDelete}
		}
	}
	return Key{Kind: KeyEsc}
}

// Reader decodes keys from a terminal (issue #336), waiting up to a short
// delay after an ESC for the rest of an escape sequence that arrives in
// pieces (a slow or remote link), as tcell does; a lone ESC is reported
// once the delay passes with nothing more.
type Reader struct {
	chunks <-chan []byte
	err    error // the read error that ended the pump, once chunks is drained
	errc   <-chan error
	buf    []byte
	last   int // the index ReadByte last returned, for UnreadByte; -1 none
	delay  time.Duration
}

// DefaultEscapeDelay is how long a Reader waits after an ESC for more bytes.
const DefaultEscapeDelay = 50 * time.Millisecond

// NewReader starts reading r on a goroutine (it runs until r returns an
// error) and decodes keys from it; delay <= 0 means DefaultEscapeDelay.
func NewReader(r io.Reader, delay time.Duration) *Reader {
	if delay <= 0 {
		delay = DefaultEscapeDelay
	}
	chunks := make(chan []byte, 16)
	errc := make(chan error, 1)
	go func() {
		for {
			b := make([]byte, 256)
			n, err := r.Read(b)
			if n > 0 {
				chunks <- b[:n]
			}
			if err != nil {
				errc <- err
				close(chunks)
				return
			}
		}
	}()
	return &Reader{chunks: chunks, errc: errc, delay: delay, last: -1}
}

// ReadKey blocks for the next key; see the package-level ReadKey for the
// decoding. After the underlying reader fails, it returns that error.
func (r *Reader) ReadKey() (Key, error) {
	r.last = -1 // UnreadByte only undoes a byte of this key
	return readKey(r)
}

// ReadByte returns the next byte, blocking until one arrives.
func (r *Reader) ReadByte() (byte, error) {
	for len(r.buf) == 0 {
		if !r.fill(0) {
			return 0, r.err
		}
	}
	b := r.buf[0]
	r.buf = r.buf[1:]
	r.last = int(b)
	return b, nil
}

// UnreadByte puts back the byte ReadByte last returned.
func (r *Reader) UnreadByte() error {
	if r.last < 0 {
		return bufio.ErrInvalidUnreadByte
	}
	r.buf = append([]byte{byte(r.last)}, r.buf...)
	r.last = -1
	return nil
}

// More reports whether a byte is buffered or arrives within the delay.
func (r *Reader) More() bool {
	return len(r.buf) > 0 || r.fill(r.delay)
}

// fill appends the next chunk to buf, waiting at most wait (0: forever); it
// reports whether one arrived, recording the pump's error once it ends.
func (r *Reader) fill(wait time.Duration) bool {
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case c, ok := <-r.chunks:
		if !ok {
			if r.err == nil {
				r.err = <-r.errc
			}
			return false
		}
		r.buf = append(r.buf, c...)
		return true
	case <-timeout:
		return false
	}
}
