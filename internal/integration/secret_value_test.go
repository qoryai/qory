package integration_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/integration"
)

// secret is a value that contains an access key secret; it is not a real key.
const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// TestAProgramPathWithASecretIsNotPrinted describes a program whose path contains a
// secret, one that fails with a run key on its standard error and one that is not
// there: each error prints Forager's phrase in place of the path and the word, and
// still unwraps to the error of the run.
func TestAProgramPathWithASecretIsNotPrinted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), secret)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "describe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'refused qrk_m' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := integration.Describe(context.Background(), path)
	want := "(a value that contains an access key secret) describe: exit status 1: refused (a value that contains a run key)"
	if err == nil || err.Error() != want {
		t.Errorf("failing program: %v, want %q", err, want)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Error("the error lost the exit error under it")
	}
	_, err = integration.Describe(context.Background(), filepath.Join(dir, "gone"))
	if err == nil || strings.Contains(err.Error(), secret) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing program: %v (errors.Is ErrNotExist %v)", err, errors.Is(err, os.ErrNotExist))
	}
}

// TestASettingNameWithASecretIsNotPrinted checks settings against a schema whose secret
// setting, and whose required one, has a name with a secret: each refusal prints
// Forager's phrase in place of the name.
func TestASettingNameWithASecretIsNotPrinted(t *testing.T) {
	const name = "(a name that contains an access key secret)"
	d, err := integration.Describe(context.Background(), program(t, withSettings(t, `{"type": "object", "properties": {"`+secret+`": {"type": "string", "writeOnly": true}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	err = d.CheckSettings([]byte(`{"` + secret + `": "x"}`))
	if want := "settings." + name + " is a secret, and the settings go on a command line; set " + name + ", the path of a file that contains it, in its place"; err == nil || err.Error() != want {
		t.Errorf("secret setting: %v, want %q", err, want)
	}
	d, err = integration.Describe(context.Background(), program(t, withSettings(t, `{"type": "object", "required": ["`+secret+`"]}`)))
	if err != nil {
		t.Fatal(err)
	}
	err = d.CheckSettings([]byte(`{}`))
	if err == nil || strings.Contains(err.Error(), secret) || !strings.HasSuffix(err.Error(), "settings."+name+" is required") {
		t.Errorf("required setting: %v", err)
	}
}
