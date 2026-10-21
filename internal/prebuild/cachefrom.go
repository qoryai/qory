package prebuild

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The words of a refused build.cacheFrom. Each is one format string of the
// configuration's path.
const (
	textCacheFrom     = "%s sets build.cacheFrom to a cache other than an image in a registry; the build takes its cache only from a registry"
	textCacheFromForm = "%s sets build.cacheFrom to something other than a string or an array of strings"
	textCacheFromRead = "%s sets build.cacheFrom to an entry that holds ${, a quote or a line break, which devcontainer build or buildx may read other than as written; no image is built from it"
)

// checkCacheFrom refuses build.cacheFrom, raw as the configuration at where writes it,
// when an entry of it names a cache of another type than registry: a folder of the
// machine, as type=local does, or a service, as type=gha and type=s3 do, which buildx
// gives credentials of the job's environment.
//
// The devcontainer command passes a string, or each string of an array, as one
// --cache-from of docker build (spec-node/singleContainer.ts), and buildx reads each
// (buildx v0.33.0 util/buildflags/cache.go, ParseCacheEntry): one with no = is a list of
// image references, each a registry cache, and one with = a list of key=value fields of
// which type= names the type. An entry is checked as written: one that holds a
// variable, ${, a quote, which the fields' CSV form gives a meaning, or a line break is
// refused, since what reaches buildx then is not what is checked.
func checkCacheFrom(where string, raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return refused(fmt.Errorf(textCacheFromForm, where))
	}
	var entries []string
	switch v := v.(type) {
	case string:
		entries = []string{v}
	case []any:
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return refused(fmt.Errorf(textCacheFromForm, where))
			}
			entries = append(entries, s)
		}
	case nil:
	default:
		return refused(fmt.Errorf(textCacheFromForm, where))
	}
	for _, e := range entries {
		if strings.Contains(e, "${") || strings.ContainsAny(e, "\"\r\n") {
			return refused(fmt.Errorf(textCacheFromRead, where))
		}
		if !registryCache(e) {
			return refused(fmt.Errorf(textCacheFrom, where))
		}
	}
	return nil
}

// registryCache reports whether entry, one --cache-from that holds no ${, quote or line
// break, names only registry caches.
func registryCache(entry string) bool {
	if entry == "" {
		return true
	}
	if !strings.Contains(entry, "=") {
		return true
	}
	typed := false
	for _, f := range strings.Split(entry, ",") {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return false
		}
		if strings.ToLower(k) == "type" {
			if v != "registry" {
				return false
			}
			typed = true
		}
	}
	return typed
}
