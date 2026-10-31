package cmd

import (
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/image"
)

// TestTheProbeTakesEachRuntimesProgramFromItsDefinition is the probe's arguments: each
// runtime gets the program its launch template starts and its package at the version,
// a runtime with no launch template and one this qory does not render get no program,
// and a runtime with no version gets no package.
func TestTheProbeTakesEachRuntimesProgramFromItsDefinition(t *testing.T) {
	got := probeRuntimes([]string{"claude=2.1.316", "cursor=", "goose=1.0.0", "example=1"})
	want := []image.Runtime{
		{Name: "claude", Command: "claude", Version: "2.1.316", Install: "@anthropic-ai/claude-code@2.1.316"},
		{Name: "cursor", Command: "cursor-agent"},
		{Name: "goose", Version: "1.0.0"},
		{Name: "example", Version: "1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}
