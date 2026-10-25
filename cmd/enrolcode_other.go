//go:build !unix

package cmd

import "os"

// openEnrolmentCode opens file for reading; [readEnrolmentCode] has checked that it is
// no link.
func openEnrolmentCode(file string) (*os.File, error) { return os.Open(file) }

// linkRefused is false: [openEnrolmentCode] opens no file with a rule for links.
func linkRefused(error) bool { return false }
