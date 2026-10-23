//go:build windows

package main

import "os"

// shutdownSignals lists the signals that ask the sender to close its room.
// Windows has no SIGTERM; os.Interrupt (Ctrl+C) is the only graceful stop.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// selfInterrupt delivers an interrupt to this process, waking signal handlers.
func selfInterrupt() error {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return p.Signal(os.Interrupt)
}
