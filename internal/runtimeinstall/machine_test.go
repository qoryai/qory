package runtimeinstall_test

import (
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
)

// TestTheMachineLinesReadBackAsWritten writes a pool's registry and runtimes folder as
// lines after a run code, and reads them back; a machine with neither has no line.
func TestTheMachineLinesReadBackAsWritten(t *testing.T) {
	m := runtimeinstall.Machine{Registry: "registry.example:5000", Folder: "/var/lib/qory-pool/runtimes"}
	lines := m.Lines()
	if lines != "registry registry.example:5000\nruntimes /var/lib/qory-pool/runtimes\n" {
		t.Fatalf("lines = %q", lines)
	}
	got, ok := runtimeinstall.ParseMachine([]string{"registry registry.example:5000", "runtimes /var/lib/qory-pool/runtimes"})
	if !ok || got != m {
		t.Fatalf("parsed %+v %t, want %+v", got, ok, m)
	}
	if (runtimeinstall.Machine{}).Lines() != "" {
		t.Fatal("an empty machine has lines")
	}
}

// TestTheMachineLinesRefuse reads lines after a run code that qory run refuses.
func TestTheMachineLinesRefuse(t *testing.T) {
	for _, lines := range [][]string{
		{""},
		{"registry registry.example", "registry other.example"},
		{"registry registry.example/qory-env"},
		{"registry registry"},
		{"runtimes relative/folder"},
		{"runtimes /var/lib/../runtimes"},
		{"runtimes /var/lib/runtimes\r"},
		{"folder /var/lib/runtimes"},
		{"registry"},
	} {
		if m, ok := runtimeinstall.ParseMachine(lines); ok {
			t.Errorf("%q read as %+v, want a refusal", lines, m)
		}
	}
}

// TestTheRegistryHostIsTheImagesFirstPart reads the registry of pool.registry.
func TestTheRegistryHostIsTheImagesFirstPart(t *testing.T) {
	for image, want := range map[string]string{
		"localhost:5001/qory-env":        "localhost:5001",
		"registry.example/qory-env":      "registry.example",
		"localhost/qory-env":             "localhost",
		"qory-env":                       "",
		"library/qory-env":               "",
		"registry.example/":              "",
		"user@registry.example/qory-env": "",
	} {
		if got := runtimeinstall.RegistryHost(image); got != want {
			t.Errorf("RegistryHost(%q) = %q, want %q", image, got, want)
		}
	}
}
