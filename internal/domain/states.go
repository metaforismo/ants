package domain

// transitionTable encodes an explicit, closed set of legal state transitions.
// State machines are data, not scattered switch statements: tests enumerate the
// whole table so adding a state without wiring its edges fails review.
type transitionTable[S ~string] map[S][]S

func (t transitionTable[S]) allows(from, to S) bool {
	for _, next := range t[from] {
		if next == to {
			return true
		}
	}
	return false
}

func (t transitionTable[S]) edgesFrom(from S) []S {
	out := make([]S, len(t[from]))
	copy(out, t[from])
	return out
}

// checkTransitionTable asserts internal consistency: every source and target
// must be a declared state of the machine, and every state must appear as a
// source (terminal states have empty edge lists).
func checkTransitionTable[S ~string](states []S, table transitionTable[S]) error {
	known := make(map[S]bool, len(states))
	for _, s := range states {
		known[s] = true
	}
	seenSource := make(map[S]bool, len(states))
	for from, targets := range table {
		if !known[from] {
			return Invalidf("state_machine", "transition source %q is not a declared state", from)
		}
		seenSource[from] = true
		for _, to := range targets {
			if !known[to] {
				return Invalidf("state_machine", "transition target %q is not a declared state", to)
			}
		}
	}
	for _, s := range states {
		if !seenSource[s] {
			return Invalidf("state_machine", "state %q has no entry in the transition table", s)
		}
	}
	return nil
}
