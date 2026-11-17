package runtimeinstall_test

import (
	"strconv"
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
	with := runtimeinstall.Machine{Registry: "registry.example:5000", Instance: "https://apiary.example.com", Folder: "/var/lib/qory-pool/runtimes"}
	if want := "registry registry.example:5000\napiary https://apiary.example.com\nruntimes /var/lib/qory-pool/runtimes\n"; with.Lines() != want {
		t.Fatalf("lines = %q, want %q", with.Lines(), want)
	}
	got, ok = runtimeinstall.ParseMachine([]string{"registry registry.example:5000", "apiary https://apiary.example.com", "runtimes /var/lib/qory-pool/runtimes"})
	if !ok || got != with {
		t.Fatalf("parsed %+v %t, want %+v", got, ok, with)
	}
	// The instance's address is written only with a registry, which it checks the realm of.
	if got := (runtimeinstall.Machine{Instance: "https://apiary.example.com", Folder: "/x"}).Lines(); got != "runtimes /x\n" {
		t.Fatalf("lines = %q, want the folder alone", got)
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
		{"registry registry.example?x=1"},
		{"registry registry.example#fragment"},
		{"registry registry.example%2fx"},
		{"registry user@registry.example"},
		{"registry registry.example/path"},
		{"registry registry.example "},
		{"registry  registry.example"},
		{"registry registry.example\t"},
		{"registry registry.example:0"},
		{"registry registry.example:65536"},
		{"registry registry.example:port"},
		{"registry registry.example:"},
		{"registry registry.example:5000:5000"},
		{"registry [::1]:5000"},
		{"registry registry_.example"},
		{"apiary"},
		{"apiary http://apiary.example.com"},
		{"apiary https://user@apiary.example.com"},
		{"apiary https://user:secret@apiary.example.com"},
		{"apiary https://apiary.example.com?x=1"},
		{"apiary https://apiary.example.com#x"},
		{"apiary https://apiary.example.com/"},
		{"apiary https://apiary.example.com/registry/token"},
		{"apiary https://apiary_.example.com"},
		{"apiary https://apiary.example.com:0"},
		{"apiary https://apiary.example.com:65536"},
		{"apiary https://apiary.example.com%2f"},
		{"apiary https://[::1]:4000"},
		{"apiary ftp://apiary.example.com"},
		{"apiary apiary.example.com"},
		{"apiary  https://apiary.example.com"},
		{"apiary https://apiary.example.com", "apiary https://apiary.example.com"},
		{"apiary https://apiary.example.com", "apiary https://other.example.com"},
	} {
		if m, ok := runtimeinstall.ParseMachine(lines); ok {
			t.Errorf("%q read as %+v, want a refusal", lines, m)
		}
	}
}

// TestTheMachineLinesTakeAnInstanceAddress reads apiary lines with an https URL, with a
// port, and an http URL on loopback.
func TestTheMachineLinesTakeAnInstanceAddress(t *testing.T) {
	for _, address := range []string{"https://apiary.example.com", "https://apiary.example.com:8443", "http://127.0.0.1:4000", "http://localhost:4000"} {
		m, ok := runtimeinstall.ParseMachine([]string{"apiary " + address})
		if !ok || m.Instance != address {
			t.Errorf("apiary %s read as %+v %t", address, m, ok)
		}
	}
}

// TestTheMachineLinesTakeAHostAndAPort reads registry lines with a host, an address or
// localhost, and a port from 1 to 65535.
func TestTheMachineLinesTakeAHostAndAPort(t *testing.T) {
	for _, host := range []string{"registry.example", "registry.example:5000", "localhost:5001", "127.0.0.1:5000", "registry.example:1", "registry.example:65535", "a-b.registry.example"} {
		m, ok := runtimeinstall.ParseMachine([]string{"registry " + host})
		if !ok || m.Registry != host {
			t.Errorf("registry %s read as %+v %t", host, m, ok)
		}
	}
}

// TestRegistryOfRefusesAHostThatRunsCannotTake reads the registry of pool.registry: a
// host that [runtimeinstall.CheckHost] takes is the registry, an image with no host has
// none, and a first part that is a host but is not one a run takes is an error whose first
// line is the fix and whose second line is the host.
func TestRegistryOfRefusesAHostThatRunsCannotTake(t *testing.T) {
	for image, host := range map[string]string{
		"registry.example:5000/qory-env": "registry.example:5000",
		"registry.example/qory-env":      "registry.example",
		"localhost/qory-env":             "localhost",
		"127.0.0.1:65535/qory-env":       "127.0.0.1:65535",
		"qory-env":                       "",
		"library/qory-env":               "",
		"user@host/qory-env":             "",
	} {
		got, err := runtimeinstall.RegistryOf(image)
		if err != nil || got != host || runtimeinstall.RegistryHost(image) != host {
			t.Errorf("RegistryOf(%q) = %q, %v; want %q", image, got, err, host)
		}
	}
	for image, bad := range map[string]string{
		"registry.example:0/qory-env":     "registry.example:0",
		"registry.example:65536/qory-env": "registry.example:65536",
		"registry.example:port/qory-env":  "registry.example:port",
		"user@registry.example/qory-env":  "user@registry.example",
		"[::1]:5000/qory-env":             "[::1]:5000",
		"registry_.example/qory-env":      "registry_.example",
		"registry.example?x/qory-env":     "registry.example?x",
	} {
		got, err := runtimeinstall.RegistryOf(image)
		want := "Set pool.registry to an image name on a host of letters, digits, . and -\nThe host is " + strconv.Quote(bad) + "; a port, if any, is from 1 to 65535."
		if err == nil || err.Error() != want || got != "" || runtimeinstall.RegistryHost(image) != "" {
			t.Errorf("RegistryOf(%q) = %q, %v; want the error %q", image, got, err, want)
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
