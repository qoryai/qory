package prebuild

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestCheckCacheFromTakesRegistries is build.cacheFrom of image references, as a
// string, a list of them or an array, and of type=registry: each taken.
func TestCheckCacheFromTakesRegistries(t *testing.T) {
	for _, raw := range []string{
		`"example.com/acme/shop:cache"`,
		`"acme/shop:cache,example.com/acme/shop:cache"`,
		`""`,
		`null`,
		`[]`,
		`["acme/shop:cache", "type=registry,ref=example.com/acme/shop:cache"]`,
		`"TYPE=registry,ref=acme/shop:cache"`,
		`"ref=acme/shop:cache,type=registry,mode=max"`,
	} {
		if err := checkCacheFrom(configAt, json.RawMessage(raw)); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// TestCheckCacheFromRefusesOtherCaches is an entry of any type but registry, in a string
// or in an array, and one that names no type: each refused, the value never quoted.
func TestCheckCacheFromRefusesOtherCaches(t *testing.T) {
	want := configAt + " sets build.cacheFrom to a cache other than an image in a registry; the build takes its cache only from a registry"
	for _, raw := range []string{
		`"type=local,src=/var/lib/secret"`,
		`["acme/shop:cache", "type=local,src=/"]`,
		`"type=gha"`,
		`"type=s3,region=eu-west-1,bucket=acme"`,
		`"type=azblob,name=acme"`,
		`"Type=local,src=/"`,
		`"type=Registry,ref=acme/shop:cache"`,
		`"type=registry,ref=acme/shop:cache,type=local"`,
		`"ref=acme/shop:cache"`,
		`"ref=acme/shop:cache,mode"`,
	} {
		err := checkCacheFrom(configAt, json.RawMessage(raw))
		var refusal *RefusedError
		if !errors.As(err, &refusal) || err.Error() != want {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// TestCheckCacheFromRefusesWhatItCannotReadAsWritten is an entry that holds a variable,
// a quote or a line break, which devcontainer build or buildx reads otherwise: each
// refused with a line that says so, the value never quoted.
func TestCheckCacheFromRefusesWhatItCannotReadAsWritten(t *testing.T) {
	want := configAt + " sets build.cacheFrom to an entry that holds ${, a quote or a line break, which devcontainer build or buildx may read other than as written; no image is built from it"
	for _, raw := range []string{
		`"\"type=local\",src=/"`,
		`"type=registry,ref=${localEnv:CACHE}"`,
		`["acme/shop:cache", "${containerWorkspaceFolder}"]`,
		`"type=registry,ref=acme/shop:cache\ntype=local"`,
		`"acme/shop:cache\r"`,
	} {
		err := checkCacheFrom(configAt, json.RawMessage(raw))
		var refusal *RefusedError
		if !errors.As(err, &refusal) || err.Error() != want {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// TestCheckCacheFromRefusesAnotherForm is build.cacheFrom of neither a string nor an
// array of strings: refused.
func TestCheckCacheFromRefusesAnotherForm(t *testing.T) {
	want := configAt + " sets build.cacheFrom to something other than a string or an array of strings"
	for _, raw := range []string{`1`, `{"type": "local"}`, `["acme/shop:cache", 1]`, `true`} {
		err := checkCacheFrom(configAt, json.RawMessage(raw))
		var refusal *RefusedError
		if !errors.As(err, &refusal) || err.Error() != want {
			t.Errorf("%s: %v", raw, err)
		}
	}
}
