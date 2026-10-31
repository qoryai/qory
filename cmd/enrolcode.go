package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/secretmask"
)

// The gateway of an instance enrols with no step by a person. Qory Apiary writes the
// gateway's enrolment code (qecg_…) into the file that gateway.server.enrolment_code_file
// of forager.yaml is set to. qory gateway, started with no access key, waits until the
// file exists, reads the code, and enrols with it as qory access-key enrol does. qory
// never writes or removes the file: Qory Apiary deletes it once the key is live.

// enrolmentCodeSetting is the key of forager.yaml that is set to the code's file.
const enrolmentCodeSetting = "gateway.server.enrolment_code_file"

// maxEnrolmentCodeFile is the most read from the code's file, plus one byte: a code is a
// prefix, 26 characters and one or two fingerprints, well under this.
const maxEnrolmentCodeFile = 1 << 10

// enrolmentCodePoll is the pause between two looks for the code's file; a test shortens
// it.
var enrolmentCodePoll = 2 * time.Second

// errNoCodeYet is a look for the code's file that found no file to read yet: the file or
// a folder above it does not exist, or the file was replaced between its check and its
// open.
var errNoCodeYet = errors.New("no enrolment code yet")

// enrolAtStart enrols qory gateway's access key from the code in the file that r's
// gateway.server.enrolment_code_file is set to, when r sets it and the gateway has no
// access key: none from --access-key-secret-fd, given when fd is not nil, from
// QORY_ACCESS_KEY_SECRET or in access-key-secret. It waits until the file exists, checks
// and reads it ([readEnrolmentCode]), enrols as qory access-key enrol does, without
// --print or --replace, and prints one result line, after "qory gateway:". It returns
// forager.yaml read again once the enrolment wrote it, else r. stopped is true when ctx
// ended before the key was enrolled. A secret that does not read is left to [identify],
// which stops on it as it does without the setting. No line or error contains the code,
// and the code joins the process's mask list, [secretmask.Default].
func enrolAtStart(ctx context.Context, r *config.Forager, fd *accesskey.Key, report io.Writer) (_ *config.Forager, stopped bool, _ error) {
	if r.Server.EnrolmentCodeFile == "" || fd != nil {
		return r, false, nil
	}
	dir, err := machineDir()
	if err != nil {
		return nil, false, err
	}
	if key, err := readAccessKey(dir, nil); err != nil || key != nil {
		return r, false, nil
	}
	file := r.Path(r.Server.EnrolmentCodeFile)
	code, err := waitEnrolmentCode(ctx, file, report)
	if err != nil || code == "" {
		return nil, err == nil, err
	}
	secretmask.Default().Add(code)
	// The lines enrol prints for a person at a terminal are left out: the result line
	// follows. enrol's errors are returned unchanged.
	if err := enrol(ctx, io.Discard, io.Discard, r.Server.URL, code, false, false, enrolVia{}); err != nil {
		if ctx.Err() != nil {
			return nil, true, nil
		}
		return nil, false, err
	}
	enrolled, err := config.LoadForager()
	if err != nil {
		return nil, false, input(err)
	}
	if enrolled == nil || enrolled.Server == nil {
		return nil, false, input(fmt.Errorf("restore gateway.server in %s, then start qory gateway again: the file has no gateway.server after the enrolment", config.ForagerFileName))
	}
	key, err := dir.ReadSecret()
	if err != nil {
		return nil, false, input(err)
	}
	fmt.Fprintf(report, "qory gateway: enrolled the access key %s (fingerprint %s) with the code in %s; the secret is in %s\n", enrolled.Server.AccessKeyID, key.Fingerprint(), exports.Value(file, false), dir.Path(foragerdir.SecretFile))
	return enrolled, false, nil
}

// waitEnrolmentCode returns the code in file once file exists, as [readEnrolmentCode]
// reads it, looking again every enrolmentCodePoll. Before the first pause it prints
// one line, after "qory gateway:", with the file's path. It returns "" and no error when
// ctx ends first.
func waitEnrolmentCode(ctx context.Context, file string, report io.Writer) (string, error) {
	waited := false
	for {
		code, err := readEnrolmentCode(file)
		if !errors.Is(err, errNoCodeYet) {
			return code, err
		}
		if !waited {
			fmt.Fprintf(report, "qory gateway: waiting for the enrolment code at %s; Qory Apiary writes it once this gateway is installed\n", exports.Value(file, false))
			waited = true
		}
		select {
		case <-ctx.Done():
			return "", nil
		case <-time.After(enrolmentCodePoll):
		}
	}
}

// readEnrolmentCode reads the gateway's enrolment code from file, at most
// maxEnrolmentCodeFile bytes, and returns it normalised. It is [errNoCodeYet] while file,
// or a folder above it, does not exist. The path has no link in it: its folders are the
// ones their links resolve to, and the file is no link. The file is a regular file, and it
// and every folder above it keep the rule of [config.CheckWriters]: only root and the user
// running qory may write them, but a folder that root owns with the sticky bit set,
// such as /tmp, passes, since no other user may replace a file in it. The file checked is the one opened, by device and inode,
// opened without following a link and without blocking. One newline after the code is
// left out; anything else in the file, and a code that is not a gateway's, is an error.
// Every error is input, prints the path once, and never contains the code.
func readEnrolmentCode(file string) (string, error) {
	shown := exports.Value(file, false)
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", unreadEnrolmentCode(file, err)
	}
	dir := filepath.Dir(abs)
	resolved, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNoCodeYet
	}
	if err != nil {
		return "", unreadEnrolmentCode(file, err)
	}
	if resolved != dir {
		return "", input(fmt.Errorf("set %s to a path with no link in it, such as %s: %s leads through a link", enrolmentCodeSetting, exports.Value(filepath.Join(resolved, filepath.Base(abs)), false), shown))
	}
	checked, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNoCodeYet
	}
	if err != nil {
		return "", unreadEnrolmentCode(file, err)
	}
	if checked.Mode()&fs.ModeSymlink != 0 {
		return "", input(fmt.Errorf("set %s to the file itself, not a link: %s is a link", enrolmentCodeSetting, shown))
	}
	if !checked.Mode().IsRegular() {
		return "", input(fmt.Errorf("set %s to a regular file: %s is not one", enrolmentCodeSetting, shown))
	}
	if err := config.CheckWriters(enrolmentCodeSetting, abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errNoCodeYet
		}
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return "", unreadEnrolmentCode(file, err)
		}
		return "", input(err)
	}
	f, err := openEnrolmentCode(abs)
	if errors.Is(err, fs.ErrNotExist) || linkRefused(err) {
		// Replaced since the check: the next look checks what is there now.
		return "", errNoCodeYet
	}
	if err != nil {
		return "", unreadEnrolmentCode(file, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", unreadEnrolmentCode(file, err)
	}
	if !os.SameFile(checked, opened) {
		return "", errNoCodeYet
	}
	return enrolmentCodeFrom(f, file)
}

// unreadEnrolmentCode is the error for the code's file, file, that qory could not open
// or read, for err.
func unreadEnrolmentCode(file string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return input(fmt.Errorf("set %s to a file that qory can read: %s: %w", enrolmentCodeSetting, exports.Value(file, false), err))
}

// enrolmentCodeFrom reads r, at most maxEnrolmentCodeFile bytes and one more, and
// returns the gateway's enrolment code in it, normalised, see [readEnrolmentCode].
func enrolmentCodeFrom(r io.Reader, file string) (string, error) {
	shown := exports.Value(file, false)
	b, err := io.ReadAll(io.LimitReader(r, maxEnrolmentCodeFile+1))
	defer clear(b)
	if err != nil {
		return "", unreadEnrolmentCode(file, err)
	}
	raw := bytes.TrimSuffix(b, []byte("\n"))
	switch {
	case len(raw) == 0:
		return "", input(fmt.Errorf("write the gateway's enrolment code (qecg_…) into %s: the file is empty", shown))
	case len(b) > maxEnrolmentCodeFile || bytes.ContainsAny(raw, " \t\r\n\v\f"):
		return "", input(fmt.Errorf("write only the gateway's enrolment code into %s, with at most one newline after it: the file contains more", shown))
	}
	code, err := accesskey.NormaliseCode(string(raw))
	if err != nil {
		return "", input(fmt.Errorf("write a gateway's enrolment code (qecg_…) into %s: the file does not contain an enrolment code of Qory Apiary", shown))
	}
	if kind, _ := accesskey.CodeKind(code); kind != "gateway" {
		return "", input(fmt.Errorf("write a gateway's enrolment code (qecg_…) into %s: the file contains the enrolment code of an access key of kind %s", shown, kind))
	}
	return code, nil
}

// enrolmentCodeFileFor stops, as input, the command command when r sets
// gateway.server.enrolment_code_file: command does not enrol from it, qory gateway alone
// does.
func enrolmentCodeFileFor(r *config.Forager, command string) error {
	if r == nil || r.Server == nil || r.Server.EnrolmentCodeFile == "" {
		return nil
	}
	return input(fmt.Errorf("remove %s from %s: qory gateway alone enrols from it, and %s does not", enrolmentCodeSetting, r.File, command))
}
