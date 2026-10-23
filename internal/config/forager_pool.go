package config

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/policy"
)

// DefaultPoolRuns is pool.runs when the file sets none: one run at a time.
const DefaultPoolRuns = 1

// ForagerPool is the pool section: what qory run pool runs by.
type ForagerPool struct {
	// Runs is how many runs the pool runs at once, at least 1; [DefaultPoolRuns] when the
	// file sets none.
	Runs int
	// Registry is the image name the builds are pushed under, such as
	// localhost:5001/qory-env: a lower-case name with no tag and no digest.
	Registry string
	// RunConfig is the absolute path each run the pool starts gets as XDG_CONFIG_HOME.
	RunConfig string
	// runsSet is whether the file sets Runs.
	runsSet bool
}

// poolSection is the pool section as written. The reader takes no other key: an unknown
// key, such as prebuilds, harness_ref or harness_path, which an earlier qory read, is an
// error.
type poolSection struct {
	Runs      *int    `yaml:"runs"`
	Registry  *string `yaml:"registry"`
	RunConfig *string `yaml:"run_config"`
}

// registryDigest is a digest of the right shape, which [poolRegistry] puts after a
// registry to read it as a reference.
const registryDigest = "@sha256:0000000000000000000000000000000000000000000000000000000000000000"

// poolImageName is how many characters the pool adds to pool.registry to name an image:
// a slash and 16 hexadecimal digits; maxImageName is the most characters of an image's
// name, Forager's.
const (
	poolImageName = 17
	maxImageName  = 255
)

// poolRegistry reports whether v is pool.registry's shape: a lower-case image name with
// no tag and no digest, as Forager reads the name of a reference, short enough to stay one
// with the pool's name of an image after it.
func poolRegistry(v string) bool {
	return policy.ImageReference(v+registryDigest) && len(v)+poolImageName <= maxImageName
}

// readPool reads the pool section: runs, at least 1, registry, an image name, and
// run_config, an absolute path. registry and run_config, which qory config lists, are
// refused without their value when they contain an access key secret or a run key. It
// reads no file: qory run pool checks what run_config contains when it starts.
func readPool(path string, p *poolSection) (*ForagerPool, error) {
	out := &ForagerPool{Runs: DefaultPoolRuns}
	if p.Runs != nil {
		if *p.Runs < 1 {
			return nil, fmt.Errorf("%s: pool.runs is below 1; a pool runs at least 1 at once", path)
		}
		out.Runs, out.runsSet = *p.Runs, true
	}
	for _, k := range []struct {
		key string
		in  *string
	}{{"registry", p.Registry}, {"run_config", p.RunConfig}} {
		if k.in == nil {
			continue
		}
		if err := accesskey.SecretInDocument(*k.in); err != nil {
			return nil, fmt.Errorf("%s: pool.%s: %w", path, k.key, err)
		}
	}
	if p.Registry == nil {
		return nil, fmt.Errorf("%s: pool.registry is required", path)
	}
	if !poolRegistry(*p.Registry) {
		return nil, fmt.Errorf("%s: pool.registry is not an image name", path)
	}
	out.Registry = *p.Registry
	if p.RunConfig == nil {
		return nil, fmt.Errorf("%s: pool.run_config is required", path)
	}
	if !filepath.IsAbs(*p.RunConfig) {
		return nil, fmt.Errorf("%s: pool.run_config is not an absolute path", path)
	}
	out.RunConfig = filepath.Clean(*p.RunConfig)
	return out, nil
}

// rows are the pool section's rows, with origin, the file, for what it sets: runs, the
// default with [Default] when the file sets none, registry and run_config.
func (p *ForagerPool) rows(origin string) []Row {
	runs := Default
	if p.runsSet {
		runs = origin
	}
	return []Row{
		{"pool.runs", strconv.Itoa(p.Runs), runs},
		{"pool.registry", p.Registry, origin},
		{"pool.run_config", p.RunConfig, origin},
	}
}
