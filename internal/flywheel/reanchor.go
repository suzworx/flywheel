package flywheel

import (
	"fmt"
	"strings"
)

// AckBreak is one chain break a reanchored event acknowledged (issue #436):
// the scan passed over it, and the ledger says who decided so and why.
type AckBreak struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Reason  string `json:"reason"`
	Session string `json:"session,omitempty"`
	Note    string `json:"note"`
}

// breakClass classifies a dangling-prev break reason: "reordered" when the
// prev is the hash of a later line of the same file, else "removed".
func breakClass(reason string) string {
	if strings.HasPrefix(reason, "reordered:") {
		return "reordered"
	}
	return "removed"
}

// reanchorAcks returns the reanchored events of dir's log. A log that does
// not read yields none: the chain scan reports that line itself.
func reanchorAcks(dir string) []Event {
	events, err := ReadEvents(dir)
	if err != nil {
		return nil
	}
	var acks []Event
	for _, e := range events {
		if e.Kind == "reanchored" {
			acks = append(acks, e)
		}
	}
	return acks
}

// ackFor returns the acknowledgement of the dangling-prev break at line
// lineNum of file (raw is the line, prev its dangling prev, reason the
// checker's reason now): a reanchored event with the same file and prev, the
// line's hash, and the same classification — a reordered acknowledgement
// never covers a break that now classifies as removed.
func ackFor(acks []Event, file string, lineNum int, raw, prev, reason string) (AckBreak, bool) {
	class := breakClass(reason)
	h := lineHash([]byte(raw))
	for _, e := range acks {
		if e.File == file && e.BreakPrev == prev && e.SHA256 == h && e.Reason == class {
			return AckBreak{File: file, Line: lineNum, Reason: class, Session: e.Session, Note: e.Note}, true
		}
	}
	return AckBreak{}, false
}

// AckText is the pass-reason suffix naming every acknowledged break, one
// "; acknowledged break at <file> line N (<reason>), by <session>: <note>"
// each, notes clipped; "" when there are none.
func (c LogChain) AckText() string {
	var b strings.Builder
	for _, a := range c.Acknowledged {
		by := a.Session
		if by == "" {
			by = "an unnamed session"
		}
		fmt.Fprintf(&b, "; acknowledged break at %s line %d (%s), by %s: %s", a.File, a.Line, a.Reason, by, clipNote(a.Note))
	}
	return b.String()
}

// Reanchor acknowledges the first unacknowledged break of dir's log chain
// with an appended, itself-chained reanchored event (issue #436): the ledger
// is never edited. It refuses (a RuleRefusal) when the chain is intact, when
// the break is not a dangling prev, and when the break classifies as removed
// unless force records that decision.
func Reanchor(dir, note, session string, force bool) (AckBreak, error) {
	if strings.TrimSpace(note) == "" {
		return AckBreak{}, fmt.Errorf("a reanchored event requires a note (why the break is explained)")
	}
	c, err := VerifyLogChain(dir)
	if err != nil {
		return AckBreak{}, err
	}
	if c.OK() {
		return AckBreak{}, &RuleRefusal{Rule: "reanchor", Fix: "the log chain is intact; nothing to re-anchor"}
	}
	where := fmt.Sprintf("%s line %d", c.File, c.BreakLine)
	if c.BreakPrev == "" || c.breakHash == "" {
		return AckBreak{}, &RuleRefusal{Rule: "reanchor", Fix: fmt.Sprintf("%s: %s; only a line whose prev matches no earlier line can be re-anchored", where, c.BreakReason)}
	}
	class := breakClass(c.BreakReason)
	if class == "removed" && !force {
		return AckBreak{}, &RuleRefusal{Rule: "reanchor", Fix: fmt.Sprintf("%s: %s; this may be a real edit or deletion of a record, not a merge reorder — re-run with --force to record the decision that it is explained", where, c.BreakReason)}
	}
	e := Event{Kind: "reanchored", Session: session, Note: note, File: c.File, LineNo: c.BreakLine, BreakPrev: c.BreakPrev, SHA256: c.breakHash, Reason: class}
	if err := AppendEvent(dir, e); err != nil {
		return AckBreak{}, err
	}
	return AckBreak{File: c.File, Line: c.BreakLine, Reason: class, Session: session, Note: note}, nil
}
