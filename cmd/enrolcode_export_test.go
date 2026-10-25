package cmd

import "time"

// SetEnrolmentCodePoll makes qory gateway look for the enrolment code's file every d, and
// returns what restores the default.
func SetEnrolmentCodePoll(d time.Duration) func() {
	before := enrolmentCodePoll
	enrolmentCodePoll = d
	return func() { enrolmentCodePoll = before }
}
