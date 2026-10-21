package main

// isLinux is false on macOS, which has no dumpable flag.
const isLinux = false

// dumpable is -1 on macOS, which has no dumpable flag.
func dumpable() int { return -1 }
