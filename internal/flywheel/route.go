package flywheel

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
)

// RouteChoice is the model flywheel run routed a dispatch to (issue #474),
// recorded on the dispatched event: the pick ("exploit" or "explore"), the
// objective, the draw and every candidate's score, in candidate order.
type RouteChoice struct {
	Model     string       `json:"model"`
	Pick      string       `json:"pick"`
	Objective string       `json:"objective"`
	Draw      float64      `json:"draw"`
	Scores    []RouteScore `json:"scores"`
}

// RouteScore is one candidate's attempts and objective score; Score is nil
// when the scoreboard has no score for it (too few samples, nothing accepted).
type RouteScore struct {
	Model    string   `json:"model"`
	Attempts int      `json:"attempts"`
	Score    *float64 `json:"score"`
}

// routeModel picks the worker's model for a fresh dispatch of task from
// w.Routing and the ledger's per-model scoreboard (issue #474). It is pure:
// the same events, worker and task always give the same choice. A candidate
// is eligible to exploit once it has MinAttempts attempts (StatsMinSample
// when 0) and a score; the best eligible one wins, ties going to config
// order. The draw, keyed on the seed, the task and its dispatch count, sends
// the dispatch to another candidate when it falls under Explore; with no
// eligible candidate every dispatch explores. Exploring prefers the
// candidates with the fewest attempts, a second draw choosing among them.
func routeModel(events []Event, w Worker, task string) RouteChoice {
	r := w.Routing
	minN := r.MinAttempts
	if minN == 0 {
		minN = StatsMinSample
	}
	rows := modelStats(events)
	scores := make([]RouteScore, len(r.Candidates))
	for i, c := range r.Candidates {
		scores[i] = RouteScore{Model: c}
		for _, row := range rows {
			if row.Adapter == w.Adapter && row.Model == c && row.Variant == w.Variant {
				scores[i].Attempts = row.Attempts
				scores[i].Score = routeScore(row, r.Objective)
				break
			}
		}
	}
	n := 0
	for _, e := range events {
		if e.Kind == "dispatched" && e.Task == task {
			n++
		}
	}
	key := r.Seed + "\x00" + task + "\x00" + strconv.Itoa(n)
	d := routeDraw(key)
	best := -1
	for i, s := range scores {
		if s.Attempts < minN || s.Score == nil {
			continue
		}
		if best < 0 || routeBetter(*s.Score, *scores[best].Score, r.Objective) {
			best = i
		}
	}
	choice := RouteChoice{Objective: r.Objective, Draw: round(d, 4), Scores: scores}
	var pool []int
	switch {
	case best < 0:
		for i := range scores {
			pool = append(pool, i)
		}
	case d < r.Explore && len(scores) > 1:
		for i := range scores {
			if i != best {
				pool = append(pool, i)
			}
		}
	default:
		choice.Model, choice.Pick = scores[best].Model, "exploit"
		return choice
	}
	fewest := -1
	var tied []int
	for _, i := range pool {
		switch a := scores[i].Attempts; {
		case fewest < 0 || a < fewest:
			fewest, tied = a, []int{i}
		case a == fewest:
			tied = append(tied, i)
		}
	}
	idx := int(routeDraw(key+"\x00explore") * float64(len(tied)))
	choice.Model, choice.Pick = scores[tied[idx]].Model, "explore"
	return choice
}

// routeScore is row's score under objective, or nil when it has none.
func routeScore(row StatsModel, objective string) *float64 {
	switch objective {
	case "cost_per_accepted":
		if row.Accepted == 0 {
			return nil
		}
		v := row.CostPerAccepted
		return &v
	case "accepted_rate":
		return row.AcceptedRate
	case "gate_pass_rate":
		return row.GatePassRate
	case "clean_rate":
		return row.CleanRate
	}
	return nil
}

// routeBetter reports whether score a strictly beats b under objective:
// lower for cost_per_accepted, higher for the rates.
func routeBetter(a, b float64, objective string) bool {
	if objective == "cost_per_accepted" {
		return a < b
	}
	return a > b
}

// routeDraw maps key to [0,1): the first 8 bytes of its sha256 as a
// big-endian uint64 over 2^64, kept to the 53 bits a float64 holds exactly so
// the value never rounds up to 1.
func routeDraw(key string) float64 {
	h := sha256.Sum256([]byte(key))
	return float64(binary.BigEndian.Uint64(h[:8])>>11) / (1 << 53)
}
