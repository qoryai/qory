package compose_test

import (
	"os"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/profile"
)

// TestExample keeps the hello example composable: the profile loads, the excluded greet
// skill comes from the world layer, and dropping the exclude is the collision the README
// promises.
func TestExample(t *testing.T) {
	p, err := profile.Load("../../examples/hello/harness-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	var greet string
	for _, e := range res.Entries {
		if e.Kind == "skills" && e.Name == "greet" {
			greet = e.Layer
		}
	}
	if greet != "world" {
		t.Fatalf("skills/greet comes from %q, want world", greet)
	}
	p.Layers[0].Exclude = nil
	_, err = compose.Compose(p)
	if err == nil || !strings.Contains(err.Error(), "skills/greet is provided by 2 layers") {
		t.Fatalf("without the exclude: %v", err)
	}
	if _, err := os.Stat("../../examples/hello/README.md"); err != nil {
		t.Fatal(err)
	}
}
