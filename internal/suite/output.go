package suite

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
)

// eventLog is the event type of one chunk of output, with the number of its step as step.
const eventLog = "dev.qory.run.log"

// logData is the members of the data of a [eventLog] event that [ReadOutput] and
// [ReadHead] read.
type logData struct {
	Step   int    `json:"step"`
	Stream string `json:"stream"`
	Bytes  string `json:"bytes"`
}

// ReadOutput reads the output of the after step number step from the events file,
// events.jsonl of the run's folder: the bytes of its dev.qory.run.log events in the order
// of the file, the last [MaxOutputBytes] of them. When that cap cuts inside a line, the
// result starts after the line break, so no masked value is cut in two, and it is the
// bytes of [NoTextLine] when no line with text starts in the last [MaxOutputBytes] bytes.
// Forager has masked the run's secrets in these bytes. A missing or unreadable file, a
// step below 1 and a step with no output return nil, as does a run whose events go
// elsewhere, such as one that a separate gateway records. A line that is not JSON ends
// the read: what came before it counts.
func ReadOutput(eventsFile string, step int) []byte {
	var kept []byte
	eachChunk(eventsFile, step, func(_ string, chunk []byte) bool {
		kept = append(kept, chunk...)
		// Cut at twice the cap, so a long output costs a copy for every cap's worth of it.
		// The byte before the last cap's worth stays, so that lastBytes sees whether a line
		// starts there.
		if len(kept) > 2*MaxOutputBytes {
			kept = append(kept[:0], kept[len(kept)-MaxOutputBytes-1:]...)
		}
		return true
	})
	return lastOutput(kept)
}

// maxHeadScan is the most bytes that the first line of the step's standard output may have
// for [ReadHead] to read it as the marker line. The line is [HeadMarker] and a commit id,
// at most 80 bytes.
const maxHeadScan = 16 << 10

// streamStdout is the stream of the [eventLog] events that carry the step's standard
// output. Its other streams, such as stderr, carry what the script's git and the suite's
// code write to standard error, which the suite's code and the files of the commit can
// fill.
const streamStdout = "stdout"

// ReadHead is the commit of the marker line of the after step number step: the first line
// of the step's standard output, the events with the stream stdout, when it is
// [HeadMarker] and a full commit id. It is "" for no such line: a first line that is
// anything else, a marker on a later line or on stderr, a marker whose commit is not a
// full commit id, and a first line that has no line end within [maxHeadScan] bytes. The
// script prints the marker before anything else to standard output, so the line is the
// script's, see [Command]. The missing, unreadable and empty cases are those of
// [ReadOutput].
func ReadHead(eventsFile string, step int) string {
	var first []byte
	ended := false
	eachChunk(eventsFile, step, func(stream string, chunk []byte) bool {
		if stream != streamStdout {
			return true
		}
		first = append(first, chunk...)
		if i := bytes.IndexByte(first, '\n'); i >= 0 {
			first, ended = first[:i], true
			return false
		}
		return len(first) < maxHeadScan
	})
	if !ended || len(first) >= maxHeadScan {
		return ""
	}
	if id, ok := strings.CutPrefix(strings.TrimSuffix(string(first), "\r"), HeadMarker); ok && commitID.MatchString(id) {
		return id
	}
	return ""
}

// eachChunk calls each with the stream and the decoded bytes of each dev.qory.run.log
// event of the step in the events file, in the order of the file, until each returns
// false.
func eachChunk(eventsFile string, step int, each func(stream string, chunk []byte) bool) {
	if step < 1 {
		return
	}
	f, err := os.Open(eventsFile)
	if err != nil {
		return
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var e eventLine
		if err := dec.Decode(&e); err != nil {
			return
		}
		if e.Type != eventLog {
			continue
		}
		var d logData
		if json.Unmarshal(e.Data, &d) != nil || d.Step != step {
			continue
		}
		chunk, err := base64.StdEncoding.DecodeString(d.Bytes)
		if err != nil {
			continue
		}
		if !each(d.Stream, chunk) {
			return
		}
	}
}
