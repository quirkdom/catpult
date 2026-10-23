//go:build !windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals lists the signals that ask the sender to close its room.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// selfInterrupt delivers SIGINT to this process, waking signal handlers.
func selfInterrupt() error {
	return syscall.Kill(syscall.Getpid(), syscall.SIGINT)
}
