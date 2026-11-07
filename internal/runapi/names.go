package runapi

import (
	"regexp"
	"strings"
	"unicode"
)

// MaxArtifactName is the most bytes of an artifact type or an artifact name.
const MaxArtifactName = 64

// MaxFilename is the most bytes of the name of one file of a version that qory writes
// into a folder: the limit of a file's name on the file systems qory runs on.
const MaxFilename = 255

// artifactName is the form of an artifact type and of an artifact name.
var artifactName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ArtifactName reports whether value has the form of an artifact type or an artifact
// name in the run API, and in process.schema.json and module.schema.json: lowercase
// letters and digits in groups joined by single hyphens, at most [MaxArtifactName] bytes.
// The form also makes value safe as the name of a folder.
func ArtifactName(value string) bool {
	return len(value) <= MaxArtifactName && artifactName.MatchString(value)
}

// LocalFilename reports whether name is safe as the name of one file in a folder: 1 to
// [MaxFilename] bytes, neither . nor .., with no slash, backslash or control character.
func LocalFilename(name string) bool {
	return name != "" && len(name) <= MaxFilename && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) && strings.IndexFunc(name, unicode.IsControl) < 0
}
