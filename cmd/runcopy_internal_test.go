package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestAFetchPastItsLimitHasItsLine is a fetch that ran past the limit: the line has the fetch,
// and a stop keeps the error for the line of the stop.
func TestAFetchPastItsLimitHasItsLine(t *testing.T) {
	limited, cancel := context.WithCancel(context.Background())
	cancel()
	failed := errors.New("the fetch failed")
	err := limitErr(context.Background(), limited, textCopyFetchLimit, failed)
	want := "the fetch of the run's commits ran past its limit of " + copyStepLimit.String()
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("the line is %v", err)
	}
	stop, cancelStop := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelStop()
	<-stop.Done()
	if err := limitErr(stop, limited, textCopyFetchLimit, failed); !errors.Is(err, failed) {
		t.Errorf("after a stop the error is %v", err)
	}
}

// TestASignalInTheCheckOrFetchHasItsLine is a stop that ended the check of a run's commits or
// their fetch, which share the line: the line is true for both.
func TestASignalInTheCheckOrFetchHasItsLine(t *testing.T) {
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	c := &runCopy{cutBy: func() string { return "qory run got a second signal" }}
	err := c.checkFailed(stopped, errors.New("the fetch failed"))
	want := "qory run got a second signal during the check or the fetch of the run's commits, so those commits did not come back"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("the line is %v", err)
	}
}
