package suite

import (
	"encoding/json"
	"math"
	"net/netip"
	"os"
	"regexp"
	"sort"
	"strconv"
)

// The event types that [ReadRefused] reads, and the step kind of the after step.
const (
	eventStepStarted = "dev.qory.run.step_started"
	eventStepExited  = "dev.qory.run.step_exited"
	eventEgress      = "dev.qory.run.egress"
	stepKindAfter    = "after"
	decisionDenied   = "denied"
)

// Refused is what the run's events have about the connections that the proxy refused while the
// after step ran.
type Refused struct {
	// Local is whether the events file has the after step. A run whose events go elsewhere,
	// such as one that a separate gateway records, has no such file here.
	Local bool
	// Denied is the number of refused connections of the step, hosts that [Refused.Hosts]
	// leaves out included.
	Denied int
	// Hosts are the hosts of those connections that have the form of a name or an address,
	// each once, in the order of their first refusal.
	Hosts []string
}

// hostName is the form of a host name: letters, digits, hyphens and dots, 253 characters
// at most, starting and ending with a letter or a digit.
var hostName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

// validHost reports whether host is a name or an address in the forms the proxy records
// when it knows the host: a host as the session sent it, which can hold any character, is
// not shown.
func validHost(host string) bool {
	if hostName.MatchString(host) {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.Zone() == "" && addr.String() == host
}

// eventLine is the members of a line of events.jsonl that ReadRefused reads. Data is read
// only for the three event types that it needs.
type eventLine struct {
	Type     string          `json:"type"`
	Sequence string          `json:"sequence"`
	Data     json.RawMessage `json:"data"`
}

// eventData is the members of the data of those three event types.
type eventData struct {
	Step     int    `json:"step"`
	Kind     string `json:"kind"`
	Host     string `json:"host"`
	Decision string `json:"decision"`
	Channel  string `json:"channel"`
}

// ReadRefused reads the events file, events.jsonl of the run's folder, for the connections
// that the proxy refused while the after step number step ran: events dev.qory.run.egress
// with the decision denied and no channel, whose sequence lies after the step's
// dev.qory.run.step_started of kind after and before its dev.qory.run.step_exited. Without
// an exit event the window runs to the end of the file. A missing or unreadable file, or one
// with no such step, is not Local.
func ReadRefused(eventsFile string, step int) Refused {
	f, err := os.Open(eventsFile)
	if err != nil {
		return Refused{}
	}
	defer f.Close()
	type refusal struct {
		seq  uint64
		host string
	}
	var (
		start, end uint64 = 0, math.MaxUint64
		started    bool
		refusals   []refusal
	)
	dec := json.NewDecoder(f)
	for {
		var e eventLine
		// A line that is not JSON ends the read: what came before it counts.
		if err := dec.Decode(&e); err != nil {
			break
		}
		if e.Type != eventStepStarted && e.Type != eventStepExited && e.Type != eventEgress {
			continue
		}
		seq, err := strconv.ParseUint(e.Sequence, 10, 64)
		var d eventData
		if err != nil || json.Unmarshal(e.Data, &d) != nil {
			continue
		}
		switch {
		case e.Type == eventStepStarted && d.Kind == stepKindAfter && d.Step == step:
			start, started = seq, true
		case e.Type == eventStepExited && started && d.Step == step:
			end = seq
		case e.Type == eventEgress && d.Decision == decisionDenied && d.Channel == "":
			refusals = append(refusals, refusal{seq, d.Host})
		}
	}
	if !started {
		return Refused{}
	}
	sort.SliceStable(refusals, func(i, j int) bool { return refusals[i].seq < refusals[j].seq })
	out := Refused{Local: true}
	seen := map[string]bool{}
	for _, r := range refusals {
		if r.seq <= start || r.seq >= end {
			continue
		}
		out.Denied++
		if validHost(r.host) && !seen[r.host] {
			seen[r.host] = true
			out.Hosts = append(out.Hosts, r.host)
		}
	}
	return out
}
