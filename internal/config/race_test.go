//go:build race

package config_test

// raceDetector is set when the tests run under the race detector, which makes the
// code it watches many times slower: a bound on how long a read takes is not held then.
const raceDetector = true
