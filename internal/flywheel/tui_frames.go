package flywheel

import (
	"fmt"
	"strings"
	"time"

	"github.com/suzworx/flywheel/internal/term"
)

// Frame is one headless render of the interactive factory: the key that led
// to it ("" for the first) and the ANSI text of the view.
type Frame struct {
	Key   string `json:"key"`
	Frame string `json:"frame"`
}

// namedKeys are the <name> tokens ParseKeySeq knows, the names the TUI's
// hints and help already use.
var namedKeys = map[string]term.Key{
	"enter":     {Kind: term.KeyEnter},
	"esc":       {Kind: term.KeyEsc},
	"tab":       {Kind: term.KeyTab},
	"backspace": {Kind: term.KeyBackspace},
	"delete":    {Kind: term.KeyDelete},
	"up":        {Kind: term.KeyUp},
	"down":      {Kind: term.KeyDown},
	"left":      {Kind: term.KeyLeft},
	"right":     {Kind: term.KeyRight},
	"pgup":      {Kind: term.KeyPgUp},
	"pgdn":      {Kind: term.KeyPgDn},
	"home":      {Kind: term.KeyHome},
	"end":       {Kind: term.KeyEnd},
	"space":     {Kind: term.KeyRune, Rune: ' '},
	"ctrl-c":    {Kind: term.KeyCtrlC},
}

// SeqKey is one token of a parsed sequence, the name it was written as, and
// its keys: one, or each rune of a quoted run.
type SeqKey struct {
	Name string
	Keys []term.Key
}

// ParseKeySeq parses a --keys sequence: space-separated tokens, each a
// single rune ("j", "/", ":"), a named key ("<enter>", "<esc>", "<up>",
// "<ctrl-a>" … "<ctrl-z>") or a double-quoted run of runes typed one by one
// ("\"andon\""). An unknown token is an error naming it.
func ParseKeySeq(seq string) ([]SeqKey, error) {
	var out []SeqKey
	rs := []rune(seq)
	for i := 0; i < len(rs); {
		if rs[i] == ' ' || rs[i] == '\t' {
			i++
			continue
		}
		if rs[i] == '"' {
			end := strings.IndexRune(string(rs[i+1:]), '"')
			if end < 0 {
				return nil, fmt.Errorf("unterminated quoted run in %q", seq)
			}
			run := []rune(string(rs[i+1:])[:end])
			tok := SeqKey{Name: string(rs[i : i+len(run)+2])}
			for _, r := range run {
				tok.Keys = append(tok.Keys, term.Key{Kind: term.KeyRune, Rune: r})
			}
			out = append(out, tok)
			i += len(run) + 2
			continue
		}
		j := i
		for j < len(rs) && rs[j] != ' ' && rs[j] != '\t' {
			j++
		}
		tok := string(rs[i:j])
		i = j
		k, err := parseKeyToken(tok)
		if err != nil {
			return nil, err
		}
		out = append(out, SeqKey{Name: tok, Keys: []term.Key{k}})
	}
	return out, nil
}

// parseKeyToken is one unquoted token of ParseKeySeq.
func parseKeyToken(tok string) (term.Key, error) {
	if r := []rune(tok); len(r) == 1 {
		return term.Key{Kind: term.KeyRune, Rune: r[0]}, nil
	}
	name, ok := strings.CutPrefix(tok, "<")
	if name, ok2 := strings.CutSuffix(name, ">"); ok && ok2 {
		name = strings.ToLower(name)
		if k, ok := namedKeys[name]; ok {
			return k, nil
		}
		if c, ok := strings.CutPrefix(name, "ctrl-"); ok && len(c) == 1 && c[0] >= 'a' && c[0] <= 'z' {
			return term.Key{Kind: term.KeyCtrl, Rune: rune(c[0])}, nil
		}
	}
	return term.Key{}, fmt.Errorf("unknown key %q: a single character, a quoted run or one of <enter> <esc> <tab> <up> <down> <left> <right> <pgup> <pgdn> <home> <end> <backspace> <delete> <space> <ctrl-a>…<ctrl-z>", tok)
}

// TUIFrames renders the interactive factory for dir headlessly: the same
// NewTUI, Update and View the live view runs, fed by TUIFetcher at the fixed
// clock now, with the default skin and colour on, at width x height. It
// returns the first frame (Key "") and one frame after each token of keys
// (ParseKeySeq; a quoted run is typed rune by rune, then drawn once). Fetches run synchronously after every key, so the frames
// never show "loading…"; a key that switches context (:ctx) stays put.
func TUIFrames(dir, keys string, width, height int, now time.Time) ([]Frame, error) {
	seq, err := ParseKeySeq(keys)
	if err != nil {
		return nil, err
	}
	clock := func() time.Time { return now }
	fetch := TUIFetcher(dir, clock)
	m := NewTUI()
	m.now = clock
	get := func() (TUIData, error) {
		snap := *m // as the live loop does: the fetch reads a copy
		m.refresh = false
		return fetch(&snap)
	}
	data, err := get()
	if err != nil {
		return nil, err
	}
	m.Observe(data)
	frames := []Frame{{Key: "", Frame: m.View(data, width, height, true)}}
	for _, k := range seq {
		for _, key := range k.Keys {
			m.Update(key, data)
			m.TakeCtx()
		}
		if data, err = get(); err != nil {
			return nil, err
		}
		m.Observe(data)
		frames = append(frames, Frame{Key: k.Name, Frame: m.View(data, width, height, true)})
	}
	return frames, nil
}
