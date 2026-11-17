package suite_test

import (
	"encoding/json"
	"testing"

	"github.com/qoryai/qory/internal/suite"
)

func TestLimitMinutesReadsFullSuiteFromTheHarnessExtensions(t *testing.T) {
	ext := func(v any) map[string]any {
		return map[string]any{"software-factory": map[string]any{"command_limits_minutes": map[string]any{"full_suite": v}}}
	}
	for _, c := range []struct {
		name string
		in   map[string]any
		want int
	}{
		{"none", nil, 60},
		{"other extension", map[string]any{"team": map[string]any{}}, 60},
		{"no full_suite", map[string]any{"software-factory": map[string]any{"command_limits_minutes": map[string]any{"fast_checks": 3}}}, 60},
		{"int", ext(5), 5},
		{"float of JSON", ext(float64(12)), 12},
		{"json number", ext(json.Number("7")), 7},
		{"60", ext(60), 60},
		{"above 60", ext(61), 60},
		{"zero", ext(0), 60},
		{"fraction", ext(2.5), 60},
		{"text", ext("5"), 60},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := suite.LimitMinutes(c.in); got != c.want {
				t.Errorf("LimitMinutes = %d, want %d", got, c.want)
			}
		})
	}
}
