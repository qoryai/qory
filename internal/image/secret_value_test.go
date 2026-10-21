package image_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/image"
)

// secret is a value that contains an access key secret; it is not a real key.
const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// TestAnImageCheckHidesASecret refuses an image reference with a secret, reads a helper
// at a path with one, and probes an image whose HOME and PATH hold one and whose git
// fails with one on its output: no message contains the secret.
func TestAnImageCheckHidesASecret(t *testing.T) {
	if err := image.CheckRef("-" + secret); err == nil || err.Error() != "(a value that contains an access key secret) is not an image reference" {
		t.Errorf("reference: %v", err)
	}
	_, err := image.HelperArch(filepath.Join(t.TempDir(), secret, "qory"))
	if err == nil || strings.Contains(err.Error(), secret) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("helper: %v (errors.Is ErrNotExist %v)", err, errors.Is(err, fs.ErrNotExist))
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, secret), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git", "claude"} {
		if err := os.WriteFile(filepath.Join(root, secret, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{"HOME": "/home/" + secret, "PATH": "/" + secret}
	p := image.Probe{
		Root: root,
		Env:  func(name string) string { return env[name] },
		Exec: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("token qrk_m refused\n"), errors.New("exit status 1")
		},
		User: "1000:1000",
	}
	rep := p.Report(context.Background())
	for _, c := range rep.Checks {
		if strings.Contains(c.Detail, secret) || strings.Contains(c.Detail, "qrk_") {
			t.Errorf("%s line %q contains the secret", c.Name, c.Detail)
		}
	}
}
