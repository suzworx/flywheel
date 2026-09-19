// Package term is flywheel's minimal terminal layer (issue #336): raw mode,
// key decoding and window size, standard library only.
package term

import (
	"bufio"
	"strings"
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
}

// ReadKey reads one key from r. Escape sequences: ESC [ A/B/C/D (arrows),
// ESC O A/B/C/D (the same), ESC [ H / ESC [ F / ESC [ 1~ / ESC [ 7~ (Home),
// ESC [ 4~ / ESC [ 8~ (End), ESC [ 5~ (PgUp), ESC [ 6~ (PgDn), ESC [ 3~
// (Delete). A lone ESC — nothing buffered after it — is KeyEsc; an unknown
// sequence is consumed and reported as KeyEsc. 13 or 10 is KeyEnter, 127 or 8
// KeyBackspace, 9 KeyTab, 3 KeyCtrlC; any other byte starts a UTF-8 rune
// (KeyRune). io errors are returned as is.
func ReadKey(r *bufio.Reader) (Key, error) {
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
		if r.Buffered() == 0 {
			return Key{Kind: KeyEsc}, nil
		}
		return readEscapeSeq(r)
	default:
		// UTF-8 rune
		r.UnreadByte()
		ru, _, err := r.ReadRune()
		if err != nil {
			return Key{}, err
		}
		return Key{Kind: KeyRune, Rune: ru}, nil
	}
}

// readEscapeSeq decodes what follows an ESC that arrived with more bytes:
// a CSI sequence (ESC [), an SS3 sequence (ESC O), or anything else — an
// Alt-modified key such as ESC x — which is reported as KeyEsc after
// consuming only that one byte, so the next key is never swallowed.
func readEscapeSeq(r *bufio.Reader) (Key, error) {
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
	}
	return Key{Kind: KeyEsc}, nil
}

// readCSISeq reads a CSI sequence up to and including its final byte
// (0x40–0x7E), so parameters and modifiers (ESC [ 1 ; 5 A for Ctrl-Up) are
// always consumed, and maps it: A/B/C/D/H/F by the final byte, "~" by the
// first parameter. Anything unknown is KeyEsc.
func readCSISeq(r *bufio.Reader) (Key, error) {
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
