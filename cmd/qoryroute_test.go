package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestAnUnwalledRunHasTheRouteToQory is a run with no wall whose runtime sends a request
// to QORY_HOST_URL through HTTP_PROXY: the run's forwarder hands it to qory as session 1's,
// and qory answers 404, as it serves no path. The request is recorded as an egress of the
// channel qory_host.
func TestAnUnwalledRunHasTheRouteToQory(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not installed")
	}
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	out := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, "#!/bin/sh\n"+
		"printf '%s' \"$QORY_HOST_URL\" > "+filepath.Join(out, "url")+"\n"+
		shellQuote(curl)+" -sS --max-time 30 --proxy \"$HTTP_PROXY\" -o "+filepath.Join(out, "body")+" -w '%{http_code}' -X POST -d '{}' \"$QORY_HOST_URL/v1/suites\" > "+filepath.Join(out, "status")+" 2> "+filepath.Join(out, "error")+"\n"+
		"exit 0\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	if got, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if got := read("url"); got != "http://host.qory.invalid" {
		t.Errorf("QORY_HOST_URL is %q", got)
	}
	if status := read("status"); status != "404" {
		t.Fatalf("the route answered %q: %s%s", status, read("body"), read("error"))
	}
	wants(t, read("body"), `"error":"not_found"`)
	_, evs := events(t, root)
	routed := 0
	for _, e := range evs["dev.qory.run.egress"] {
		if e["channel"] == "qory_host" && e["host"] == "host.qory.invalid" {
			routed++
		}
	}
	if routed != 1 {
		t.Errorf("%d egress events of the route, want 1: %v", routed, evs["dev.qory.run.egress"])
	}
}
