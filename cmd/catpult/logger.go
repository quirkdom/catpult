package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Logger provides standardized Vite-style log lines on stderr:
//
//	<time> [catpult] (<scope>) <message>
//
// When quiet is enabled, all non-fatal logging is silenced.
// When isTTY is enabled and NO_COLOR is not set, ANSI colors are used.
// Duplicate messages coming in rapid succession (<= 3s) are deduplicated with (xN).
type Logger struct {
	w         io.Writer
	quiet     bool
	color     bool
	mu        sync.Mutex
	lastScope string
	lastMsg   string
	lastTime  time.Time
	repeats   int
}

func newLogger(w io.Writer, quiet, isTTY bool) *Logger {
	noColor := os.Getenv("NO_COLOR") != ""
	return &Logger{
		w:     w,
		quiet: quiet,
		color: isTTY && !noColor,
	}
}

func (l *Logger) Log(scope, format string, args ...any) {
	if l == nil || l.quiet {
		return
	}
	msg := fmt.Sprintf(format, args...)
	msg = strings.TrimSpace(msg)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	// Deduplicate identical scope and message within 3 seconds
	if scope == l.lastScope && msg == l.lastMsg && now.Sub(l.lastTime) < 3*time.Second {
		l.repeats++
		l.lastTime = now
		t := now.Format("15:04:05")
		if l.color {
			const (
				reset = "\033[0m"
				dim   = "\033[2m"
				bold  = "\033[1m"
				cyan  = "\033[36m"
			)
			scopeColor := l.scopeColor(scope)
			// Move cursor up one line, clear line, and re-print with (xN)
			fmt.Fprintf(l.w, "\033[1A\033[2K\r%s%s%s %s%s[catpult]%s %s(%s)%s %s %s(x%d)%s\n",
				dim, t, reset,
				bold, cyan, reset,
				scopeColor, scope, reset,
				msg,
				dim, l.repeats, reset,
			)
		} else {
			fmt.Fprintf(l.w, "%s [catpult] (%s) %s (x%d)\n", t, scope, msg, l.repeats)
		}
		return
	}

	l.lastScope = scope
	l.lastMsg = msg
	l.lastTime = now
	l.repeats = 1

	t := now.Format("15:04:05")
	if !l.color {
		fmt.Fprintf(l.w, "%s [catpult] (%s) %s\n", t, scope, msg)
		return
	}

	const (
		reset = "\033[0m"
		dim   = "\033[2m"
		bold  = "\033[1m"
		cyan  = "\033[36m"
	)
	scopeColor := l.scopeColor(scope)
	fmt.Fprintf(l.w, "%s%s%s %s%s[catpult]%s %s(%s)%s %s\n",
		dim, t, reset,
		bold, cyan, reset,
		scopeColor, scope, reset,
		msg,
	)
}

func (l *Logger) scopeColor(scope string) string {
	switch scope {
	case "warn":
		return "\033[33m" // yellow
	case "error":
		return "\033[31m" // red
	case "watch":
		return "\033[32m" // green
	case "recv":
		return "\033[34m" // blue
	default:
		return "\033[36m" // cyan
	}
}

func humanizeBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"kB", "MB", "GB", "TB"}
	val := float64(b) / float64(div)
	if val < 10 {
		return fmt.Sprintf("%.1f %s", val, units[exp])
	}
	return fmt.Sprintf("%.0f %s", val, units[exp])
}
