package compose_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
)

// TestExample keeps the hello example composable: copied into a repository of its own,
// the way qory setup example writes it, the stack in its qory.yaml loads, the excluded greet skill comes
// from the world module, and dropping the exclude is the collision the README promises.
func TestExample(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("cp", "-R", "../../examples/hello/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("copy: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	p, err := config.LoadStack(filepath.Join(dir, config.FileName))
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
			greet = e.Module
		}
	}
	if greet != "world" {
		t.Fatalf("skills/greet comes from %q, want world", greet)
	}
	p.Modules[0].Exclude = nil
	_, err = compose.Compose(p)
	if err == nil || !strings.Contains(err.Error(), "skills/greet is provided by 2 modules") {
		t.Fatalf("without the exclude: %v", err)
	}
	if _, err := os.Stat("../../examples/hello/README.md"); err != nil {
		t.Fatal(err)
	}
}
