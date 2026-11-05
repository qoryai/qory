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
