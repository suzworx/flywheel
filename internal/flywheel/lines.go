package flywheel

// LineFor resolves the product line of a unit from its brief header: the
// line the header names (ok false and an error when no such line exists),
// else the first line in config order whose Owns cover every one of the
// header's Owns (ownsContains), else no line (Line{}, false, nil). A header
// with no owns and no line: entry belongs to no line.
func (c Config) LineFor(h BriefHeader) (Line, bool, error) {
	// If a line is explicitly named in the header, resolve it.
	if h.Line != "" {
		for _, l := range c.Lines {
			if l.Name == h.Line {
				return l, true, nil
			}
		}
		return Line{}, false, &RuleRefusal{Rule: "line", Fix: "no product line named " + h.Line + " in .flywheel/config.json lines[]"}
	}

	// If no line is named, find the first line whose owns: covers all of the header's owns:.
	if len(h.Owns) > 0 {
		for _, l := range c.Lines {
			if lineCoversOwns(l, h.Owns) {
				return l, true, nil
			}
		}
	}

	// No line found.
	return Line{}, false, nil
}

// lineCoversOwns reports whether a line's owns entries cover all of the
// header's owns entries: each header entry must be contained by at least
// one line owns entry (using ownsContains).
func lineCoversOwns(l Line, headerOwns []string) bool {
	for _, ho := range headerOwns {
		covered := false
		for _, lo := range l.Owns {
			if ownsContains([]string{lo}, ho) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}
