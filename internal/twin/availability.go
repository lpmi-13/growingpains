package twin

import "math"

// The search CPU cost before trigram indexes, measured on the playground. Timeout risk was
// calibrated on that query; the indexed query's lower service time reduces its tail.
const unindexedSearchMS = 50.0

// searchTimeoutUnderQuota estimates the chance that a search routed to the sale's replica
// exceeds the crowd's two-second read deadline. It is an empirical risk curve, not a queueing
// guarantee. The 17 November playground run had no search failures below about 70% of a quota,
// 1.1% of all searches failed at 76% on the minute before the replica was uncapped, and 13.5%
// failed while the cache was rolling out near 90%. About 60% of searches used the replica, so
// those last two points imply roughly 1.7% and 20% conditional failure. The separate fixed-rate
// benchmark found search p99 near two seconds at 80% of a 0.65-core quota, and no long tail
// without a quota (docs/calibration.md). We interpolate the two live points exponentially.
// The curve applies to the sale's replica only: the live primary had a different query mix
// and search service time, and had no observed search failures at its earlier high CPU ratio.
// The architect's CPU signal remains separate from this client-visible tail.
func searchTimeoutUnderQuota(used, cap, searchCPUFactor float64) float64 {
	if cap <= 0 || used/cap <= 0.70 {
		return 0
	}
	rho := used / cap
	p := math.Min(0.20, 0.017*math.Exp(math.Log(0.20/0.017)*(rho-0.76)/0.14))
	// For a cheaper search statement, the same queue has less time to cross the 2 s
	// deadline. This exponential-tail approximation only matters in what-if runs.
	return math.Pow(p, 1/math.Max(searchCPUFactor, 1e-9))
}

// searchReplicaShare is the share of searches sent to the replica by the read pool. Token
// reads use the same pool; a small share returns to the primary while replay catches up.
func (s *System) searchReplicaShare(tokenShare float64) float64 {
	if !s.ReplicaUp {
		return 0
	}
	return s.ReplicaPct / 100 / (1 + s.PrimaryWeight) * (1 - tokenShare*tokenNotReplayed)
}

var journeys = [...]string{"browse", "view", "search", "buy", "sell"}

// journeyRates mirrors the crowd's labels. They need not sum to State.Rate: one scheduled
// session can issue several requests, including search keystrokes and a seller's reread.
func (l load) journeyRates() map[string]float64 {
	return map[string]float64{
		"browse": l.home + l.listing,
		"view":   l.detail,
		"search": l.search + l.searchKeystrokes,
		"buy":    l.order + l.review,
		"sell":   l.create + l.patch + l.variants + l.delete + l.reread,
	}
}
