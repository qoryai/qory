package provisioner

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/qoryai/integrations/contracts"
)

// MaxLog is the most bytes, as UTF-8, of the log a machine log sends,
// machine-log.schema.json's 768 KiB. Every request body is at most
// contracts.MaxRequest bytes, as sent.
const MaxLog = 768 << 10

// hiddenLine replaces each line of a machine log that contains what looks like a
// secret.
const hiddenLine = "[hidden: the line contained what looks like an access key secret, a run key, a run code or an enrolment code]"

// MachineLog sends POST /v1/dispatches/<dispatchID>/machine-log,
// machine-log.schema.json: the log of the machine the pool started for the dispatch,
// from its start to its end. truncated is whether the caller cut the start of log.
// Before it sends the log, the client cleans and cuts it as [fitMachineLog] does, and
// sets truncated when that cut anything. Qory Apiary takes one machine log per
// dispatch, before or after the dispatch ends. A signed 204 is the answer. A refusal is
// a [*Refusal]: [ErrForbidden], a dispatch that is unknown or of another node pool;
// [ErrMachineLogExists], a dispatch that has a machine log; [ErrNotFound],
// [ErrUnsupportedRevision] or [ErrRateLimited].
func (c *Client) MachineLog(ctx context.Context, dispatchID, log string, truncated bool) error {
	log, cut := fitMachineLog(log)
	truncated = truncated || cut
	return c.toDispatch(ctx, "the machine log of "+dispatchID, kindMachineLog, dispatchID, "machine-log", "machine-log.schema.json", func(t string) any {
		return contracts.MachineLog{Version: 1, Time: t, Log: log, Truncated: truncated}
	})
}

// fitMachineLog is log as a machine log sends it: each U+0000 replaced by U+FFFD, each
// run of bytes that is not UTF-8 replaced by one U+FFFD, and each line that contains
// what looks like a secret, qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_ in any
// case, replaced by a fixed text.
// Of that, it keeps the last part such that the log is at most [MaxLog] bytes and the
// request's body at most contracts.MaxRequest bytes, as json.Marshal writes it. It
// steps back one whole character at a time, so the part starts at a character. When it
// cuts, the part starts after the first line feed in it, if one is there before its
// end, and cut is true.
func fitMachineLog(log string) (part string, cut bool) {
	return fitLog(cleanLog(log))
}

// cleanLog is log with each U+0000 replaced by U+FFFD, each run of bytes that is not
// UTF-8 replaced by one U+FFFD, and each line that contains what looks like a secret
// replaced by [hiddenLine]. A line ends after a line feed; the line feeds are kept.
func cleanLog(log string) string {
	replacement := string(utf8.RuneError)
	log = strings.ReplaceAll(strings.ToValidUTF8(log, replacement), "\x00", replacement)
	if !containsSecret(log) {
		return log
	}
	lines := strings.SplitAfter(log, "\n")
	for i, line := range lines {
		if containsSecret(line) {
			lines[i] = hiddenLine + line[len(strings.TrimRight(line, "\n")):]
		}
	}
	return strings.Join(lines, "")
}

// escapedLen is the bytes each ASCII character takes in a JSON string as json.Marshal
// writes it, and lineSepLen those of U+2028 and U+2029. Every other valid rune takes
// its UTF-8 bytes.
var (
	escapedLen [utf8.RuneSelf]int
	lineSepLen int
)

// logOverhead is the bytes of a machine log's body other than the log, with truncated
// set.
var logOverhead int

func init() {
	for r := range utf8.RuneSelf {
		b, _ := json.Marshal(string(rune(r)))
		escapedLen[r] = len(b) - 2
	}
	b, _ := json.Marshal(string(rune(0x2028)))
	lineSepLen = len(b) - 2
	b, _ = json.Marshal(contracts.MachineLog{Version: 1, Time: "2006-01-02T15:04:05Z", Truncated: true})
	logOverhead = len(b)
}

// fitLog is the last part of log, which is valid UTF-8, that is at most [MaxLog] bytes
// and whose body is at most contracts.MaxRequest bytes, as json.Marshal writes it. See
// [fitMachineLog].
func fitLog(log string) (part string, cut bool) {
	budget := contracts.MaxRequest - logOverhead
	start, size, written := len(log), 0, 0
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(log[:start])
		e := n
		switch {
		case r < utf8.RuneSelf:
			e = escapedLen[r]
		case r == 0x2028 || r == 0x2029:
			e = lineSepLen
		}
		if size+n > MaxLog || written+e > budget {
			break
		}
		size, written, start = size+n, written+e, start-n
	}
	if start == 0 {
		return log, false
	}
	if i := strings.IndexByte(log[start:], '\n'); i >= 0 && start+i+1 < len(log) {
		start += i + 1
	}
	return log[start:], true
}
