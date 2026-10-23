package pool

import (
	"bytes"
	"context"
	"log/slog"
	"sync"

	"github.com/qoryai/qory/internal/provisioner"
)

// keptLog is the most bytes of a qory run's output the pool keeps for the dispatch's
// machine log: the last part, twice the machine log's bound, which
// [provisioner.Client.MachineLog] then cleans and cuts.
const keptLog = 2 * provisioner.MaxLog

// outputTail keeps the last lines of a qory run's output, as the pool's log filter
// passes them on, each followed by a line feed: at most [keptLog] bytes once read. It
// drops whole lines from the start, and records that it dropped any.
type outputTail struct {
	mu  sync.Mutex
	buf []byte
	cut bool
}

// add keeps line and a line feed after it. The buffer is cut back to [keptLog] bytes
// once it is twice that, so a long output costs no copy per line.
func (o *outputTail) add(line string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf = append(o.buf, line...)
	o.buf = append(o.buf, '\n')
	if len(o.buf) > 2*keptLog {
		o.trim()
	}
}

// trim drops the start of the buffer, so that it is at most [keptLog] bytes and starts
// after a line feed when one is there before its end. The caller holds o.mu.
func (o *outputTail) trim() {
	if len(o.buf) <= keptLog {
		return
	}
	drop := len(o.buf) - keptLog
	if i := bytes.IndexByte(o.buf[drop:], '\n'); i >= 0 && drop+i+1 < len(o.buf) {
		drop += i + 1
	}
	n := copy(o.buf, o.buf[drop:])
	o.buf, o.cut = o.buf[:n], true
}

// text is the output kept, and whether its start was dropped.
func (o *outputTail) text() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.trim()
	return string(o.buf), o.cut
}

// uploadLog uploads log, qory run's output for the dispatch id, as the dispatch's
// machine log, once; cut is whether the start of the output was dropped. The client
// replaces each line that contains what looks like a secret and cuts the log to its
// bound first. An upload that fails or is refused is logged, and not sent again: it
// does not change how the dispatch ended.
func (p *Pool) uploadLog(work context.Context, id, log string, cut bool) {
	ids := slog.String("dispatch_id", id)
	if err := p.Client.MachineLog(work, id, log, cut); err != nil {
		p.log.Warn(lineLogNotUploaded, ids, errAttr(err))
		return
	}
	p.log.Info(lineLogUploaded, ids)
}
