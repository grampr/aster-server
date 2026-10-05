package voice

import "sort"

// sortStates orders participants by User ID so listings are stable.
func sortStates(states []State) {
	sort.Slice(states, func(i, j int) bool { return states[i].UserID.String() < states[j].UserID.String() })
}
