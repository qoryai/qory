package cmd

import (
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/image"
)

// TestTheProbeTakesEachRuntimesProgramFromItsDefinition is the probe's arguments: each
// runtime gets the program its launch template starts, and a runtime with no launch
// template and one this qory does not render get no program.
func TestTheProbeTakesEachRuntimesProgramFromItsDefinition(t *testing.T) {
	got := probeRuntimes([]string{"claude", "cursor", "goose", "example"})
	want := []image.Runtime{
		{Name: "claude", Command: "claude"},
		{Name: "cursor", Command: "cursor-agent"},
		{Name: "goose"},
		{Name: "example"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}
