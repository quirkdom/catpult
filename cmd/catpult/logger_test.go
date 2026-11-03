package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoggerPlain(t *testing.T) {
	var buf bytes.Buffer
	l := newLogger(&buf, false, false)
	l.Log("room", "waiting for recipients...")
	out := buf.String()
	if !strings.Contains(out, "[catpult] (room) waiting for recipients...") {
		t.Fatalf("unexpected log line: %q", out)
	}
	// Ends with newline
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("expected newline ending: %q", out)
	}
}

func TestLoggerQuiet(t *testing.T) {
	var buf bytes.Buffer
	l := newLogger(&buf, true, false)
	l.Log("room", "should not appear")
	l.Log("warn", "also suppressed")
	if buf.Len() > 0 {
		t.Fatalf("expected quiet logger to produce no output, got %q", buf.String())
	}
}

func TestLoggerColor(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	var buf bytes.Buffer
	l := newLogger(&buf, false, true)
	l.Log("watch", "update app.go (resending to 1 recipient)")
	out := buf.String()
	if !strings.Contains(out, "\033[") {
		t.Fatalf("expected ANSI color escape codes, got %q", out)
	}
	if !strings.Contains(out, "[catpult]") || !strings.Contains(out, "(watch)") {
		t.Fatalf("missing program or scope tag: %q", out)
	}
}

func TestLoggerDeduplication(t *testing.T) {
	// Plain mode
	var plainBuf bytes.Buffer
	plainLogger := newLogger(&plainBuf, false, false)
	plainLogger.Log("watch", "update TODO.md (resending to 2 recipient(s))")
	plainLogger.Log("watch", "update TODO.md (resending to 2 recipient(s))")
	out := plainBuf.String()
	if !strings.Contains(out, "(x2)") {
		t.Fatalf("expected (x2) in deduplicated output, got: %q", out)
	}

	// Color / TTY mode (in-place rewrite)
	os.Unsetenv("NO_COLOR")
	var colorBuf bytes.Buffer
	colorLogger := newLogger(&colorBuf, false, true)
	colorLogger.Log("recv", "received update for TODO.md")
	colorLogger.Log("recv", "received update for TODO.md")
	colorOut := colorBuf.String()
	if !strings.Contains(colorOut, "\033[1A") || !strings.Contains(colorOut, "(x2)") {
		t.Fatalf("expected cursor rewrite with (x2), got: %q", colorOut)
	}
}

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1023, "1023 B"},
		{1024, "1.0 kB"},
		{1536, "1.5 kB"},
		{1048576, "1.0 MB"},
		{5242880, "5.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, c := range cases {
		got := humanizeBytes(c.bytes)
		if got != c.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

func TestPrintSenderInvite(t *testing.T) {
	temp := t.TempDir()
	f1 := filepath.Join(temp, "app.env")
	if err := os.WriteFile(f1, []byte("SECRET=123\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Piped / Non-interactive: outputs only the raw invite key
	var piped bytes.Buffer
	printSenderInvite(&piped, false, false, "tc1abcde12345", []string{f1}, 30*time.Minute, "")
	if got := piped.String(); got != "tc1abcde12345\n" {
		t.Fatalf("piped invite = %q, want %q", got, "tc1abcde12345\n")
	}

	// 2. Quiet: outputs only the raw invite key
	var quiet bytes.Buffer
	printSenderInvite(&quiet, true, true, "tc1abcde12345", []string{f1}, 30*time.Minute, "")
	if got := quiet.String(); got != "tc1abcde12345\n" {
		t.Fatalf("quiet invite = %q, want %q", got, "tc1abcde12345\n")
	}

	// 3. Interactive TTY: outputs formatted card with Invite section
	var tty bytes.Buffer
	printSenderInvite(&tty, true, false, "tc1abcde12345", []string{f1}, 30*time.Minute, "")
	ttyStr := tty.String()
	if !strings.Contains(ttyStr, "Invite:\n\n    tc1abcde12345\n\nShare this invite with recipients out of band.") {
		t.Errorf("missing formatted invite section in interactive output: %q", ttyStr)
	}
	if !strings.Contains(ttyStr, "Files (1):\n  • app.env (11 B)") {
		t.Errorf("missing files section in interactive output: %q", ttyStr)
	}
	if !strings.Contains(ttyStr, "Expires: in 30 minutes, at ") {
		t.Errorf("missing expiry in interactive output: %q", ttyStr)
	}
}
