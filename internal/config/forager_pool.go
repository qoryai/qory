package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/policy"
)

// The defaults of pool.runs and pool.prebuilds: one run at a time, which may be a build.
const (
	DefaultPoolRuns      = 1
	DefaultPoolPrebuilds = 1
)

// ForagerPool is the pool section: what qory run pool runs by. Exactly one of HarnessRef
// and HarnessPath is set.
type ForagerPool struct {
	// Runs is how many runs the pool runs at once, at least 1; [DefaultPoolRuns] when the
	// file sets none.
	Runs int
	// Prebuilds is how many of those runs may be builds of an environment at once, from 1
	// to Runs; [DefaultPoolPrebuilds] when the file sets none.
	Prebuilds int
	// Registry is the image name the builds are pushed under, such as
	// localhost:5001/qory-env: a lower-case name with no tag and no digest.
	Registry string
	// HarnessRef is the commit, 40 lower-case hexadecimal digits, or the release tag,
	// vX.Y.Z, of github.com/qoryai/qory-github whose explore-an-item harness each run
	// composes; empty when HarnessPath is set.
	HarnessRef string
	// HarnessPath is the absolute path of a checkout of github.com/qoryai/qory-github
	// whose harness/explore-an-item each run composes; empty when HarnessRef is set.
	HarnessPath string
	// RunConfig is the absolute path each run the pool starts gets as XDG_CONFIG_HOME.
	RunConfig string
	// runsSet and prebuildsSet say the file sets Runs and Prebuilds.
	runsSet, prebuildsSet bool
}

// poolSection is the pool section as written.
type poolSection struct {
	Runs        *int    `yaml:"runs"`
	Prebuilds   *int    `yaml:"prebuilds"`
	Registry    *string `yaml:"registry"`
	HarnessRef  *string `yaml:"harness_ref"`
	HarnessPath *string `yaml:"harness_path"`
	RunConfig   *string `yaml:"run_config"`
}

// harnessRef is pool.harness_ref's shape: a commit id or a release tag.
var harnessRef = regexp.MustCompile(`^([0-9a-f]{40}|v[0-9]+\.[0-9]+\.[0-9]+)$`)

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

// readPool reads the pool section: runs and prebuilds, each at least 1 and prebuilds at
// most runs, registry, an image name, exactly one of harness_ref, a commit id or a release
// tag, and harness_path, an absolute path, and run_config, an absolute path. registry,
// harness_path and run_config, which qory config lists, are refused without their value
// when they hold an access key secret or a run key. It reads no file: qory run pool checks
// what harness_path and run_config hold when it starts.
func readPool(path string, p *poolSection) (*ForagerPool, error) {
	out := &ForagerPool{Runs: DefaultPoolRuns, Prebuilds: DefaultPoolPrebuilds}
	if p.Runs != nil {
		if *p.Runs < 1 {
			return nil, fmt.Errorf("%s: pool.runs is below 1; a pool runs at least 1 at once", path)
		}
		out.Runs, out.runsSet = *p.Runs, true
	}
	if p.Prebuilds != nil {
		out.Prebuilds, out.prebuildsSet = *p.Prebuilds, true
	}
	if out.Prebuilds < 1 || out.Prebuilds > out.Runs {
		return nil, fmt.Errorf("%s: pool.prebuilds is below 1 or above pool.runs; the builds are a share of the runs", path)
	}
	for _, k := range []struct {
		key string
		in  *string
	}{{"registry", p.Registry}, {"harness_path", p.HarnessPath}, {"run_config", p.RunConfig}} {
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
	if (p.HarnessRef == nil) == (p.HarnessPath == nil) {
		return nil, fmt.Errorf("%s: the pool section needs exactly one of harness_ref and harness_path", path)
	}
	if p.HarnessRef != nil {
		if !harnessRef.MatchString(*p.HarnessRef) {
			return nil, fmt.Errorf("%s: pool.harness_ref is neither a commit id nor a release tag", path)
		}
		out.HarnessRef = *p.HarnessRef
	} else {
		if !filepath.IsAbs(*p.HarnessPath) {
			return nil, fmt.Errorf("%s: pool.harness_path is not an absolute path", path)
		}
		out.HarnessPath = filepath.Clean(*p.HarnessPath)
	}
	if p.RunConfig == nil {
		return nil, fmt.Errorf("%s: pool.run_config is required", path)
	}
	if !filepath.IsAbs(*p.RunConfig) {
		return nil, fmt.Errorf("%s: pool.run_config is not an absolute path", path)
	}
	out.RunConfig = filepath.Clean(*p.RunConfig)
	return out, nil
}

// rows are the pool section's rows, with origin, the file, for what it sets: runs and
// prebuilds, the defaults with [Default] when the file sets none, registry, harness_ref or
// harness_path, whichever is set, and run_config.
func (p *ForagerPool) rows(origin string) []Row {
	runs, prebuilds := Default, Default
	if p.runsSet {
		runs = origin
	}
	if p.prebuildsSet {
		prebuilds = origin
	}
	rows := []Row{
		{"pool.runs", strconv.Itoa(p.Runs), runs},
		{"pool.prebuilds", strconv.Itoa(p.Prebuilds), prebuilds},
		{"pool.registry", p.Registry, origin},
	}
	if p.HarnessRef != "" {
		rows = append(rows, Row{"pool.harness_ref", p.HarnessRef, origin})
	} else {
		rows = append(rows, Row{"pool.harness_path", p.HarnessPath, origin})
	}
	return append(rows, Row{"pool.run_config", p.RunConfig, origin})
}
