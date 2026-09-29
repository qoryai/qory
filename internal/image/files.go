package image

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// capabilityRecord is the PAX record a tar stream carries a file's capabilities in, the
// security.capability extended attribute, as docker export writes it.
const capabilityRecord = "SCHILY.xattr.security.capability"

// Special is what a scan of an image's files found: every regular file with the setuid
// or setgid bit, and every file with capabilities of its own, by its path in the image.
type Special struct {
	// Files is how many entries the scan read, directories and links included.
	Files int
	// Setuid are the files with the setuid or the setgid bit.
	Setuid []string
	// Capabilities are the files with a security.capability attribute.
	Capabilities []string
}

// Scan reads a tar stream of an image's whole file system, what docker export writes,
// for the headers alone. A stream that does not read as tar to its end-of-archive marker
// is an error. Whatever follows what it read, the padding after the marker or the rest
// of a stream that did not read, is read and dropped, so the writer never waits on it and
// never writes to a closed reader.
func Scan(r io.Reader) (Special, error) {
	var s Special
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			io.Copy(io.Discard, r)
			return s, nil
		}
		if err != nil {
			io.Copy(io.Discard, r)
			return s, err
		}
		s.Files++
		name := path.Join("/", h.Name)
		if (h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA) && h.Mode&0o6000 != 0 {
			s.Setuid = append(s.Setuid, name)
		}
		if _, ok := h.PAXRecords[capabilityRecord]; ok {
			s.Capabilities = append(s.Capabilities, name)
		}
	}
}

// filesShown is how many files a failed line names of each kind.
const filesShown = 5

// check is the line a scan gives.
func (s Special) check() Check {
	if len(s.Setuid) == 0 && len(s.Capabilities) == 0 {
		return Check{"files", Pass, fmt.Sprintf("none of the image's %d files is setuid or setgid or has capabilities of its own", s.Files)}
	}
	var found, mend []string
	if len(s.Setuid) > 0 {
		found = append(found, "setuid or setgid: "+shown(s.Setuid))
		mend = append(mend, "find / -xdev -type f -perm /6000 -exec chmod ug-s {} +")
	}
	if len(s.Capabilities) > 0 {
		found = append(found, "capabilities of its own: "+shown(s.Capabilities))
		mend = append(mend, "setcap -r on each")
	}
	return Check{"files", Fail, strings.Join(found, "; ") + "; remove them in the image: " + strings.Join(mend, ", and ")}
}

// shown names the first [filesShown] paths, and how many more there are.
func shown(paths []string) string {
	if len(paths) <= filesShown {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:filesShown], ", "), len(paths)-filesShown)
}
