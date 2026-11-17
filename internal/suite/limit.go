package suite

import (
	"encoding/json"
	"math"
)

// The bounds of the suite's time limit, in whole minutes: DefaultMinutes when the
// repository sets none, and MaxMinutes, the most the harness's schema lets it set.
const (
	DefaultMinutes = 60
	MaxMinutes     = 60
)

// LimitMinutes is the suite's time limit, in minutes, for the harness extensions of a run, the
// value of harness.extensions in the harness report: command_limits_minutes.full_suite under
// software-factory, a whole number from 1 to 60. With the key missing, or a value that is no
// whole number of that size, the limit is [DefaultMinutes], so a wrong value never lengthens
// it past that.
func LimitMinutes(extensions map[string]any) int {
	factory, _ := extensions["software-factory"].(map[string]any)
	limits, _ := factory["command_limits_minutes"].(map[string]any)
	minutes, ok := wholeNumber(limits["full_suite"])
	if !ok || minutes < 1 || minutes > MaxMinutes {
		minutes = DefaultMinutes
	}
	return minutes
}

// wholeNumber is v as an integer, for the number types that JSON and YAML decode to.
func wholeNumber(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), n == int64(int(n))
	case uint64:
		return int(n), n <= math.MaxInt32
	case float64:
		return int(n), n == math.Trunc(n) && math.Abs(n) < math.MaxInt32
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil && i == int64(int(i))
	}
	return 0, false
}
