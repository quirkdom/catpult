package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func devNullOut(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, f
}

func TestNoArgsIsError(t *testing.T) {
	stdout, stderr := devNullOut(t)
	if err := run(nil, devNull(t), stdout, stderr); err == nil {
		t.Fatal("expected error for no args")
	}
}

func TestBadExpiresRejected(t *testing.T) {
	stdout, stderr := devNullOut(t)
	if err := run([]string{"--expires", "bogus", "f.txt"}, devNull(t), stdout, stderr); err == nil {
		t.Fatal("expected error for bad --expires")
	}
	if err := run([]string{"--expires", "-5m", "f.txt"}, devNull(t), stdout, stderr); err == nil {
		t.Fatal("expected error for negative --expires")
	}
}

func TestDashMixedWithFilesRejected(t *testing.T) {
	stdout, stderr := devNullOut(t)
	if err := run([]string{"-", "a.txt"}, devNull(t), stdout, stderr); err == nil {
		t.Fatal("expected error for - mixed with files")
	}
}

func TestPipeWithFileOperandsRejected(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	stdout, stderr := devNullOut(t)
	if err := run([]string{"a.txt"}, r, stdout, stderr); err == nil {
		t.Fatal("expected error for piped stdin + file operands")
	} else if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTargetWithSenderIsError(t *testing.T) {
	stdout, stderr := devNullOut(t)
	if err := run([]string{"--target", "dir", "a.txt"}, devNull(t), stdout, stderr); err == nil {
		t.Fatal("expected error for --target with sender")
	}
}

func TestMissingFileIsError(t *testing.T) {
	stdout, stderr := devNullOut(t)
	err := run([]string{"definitely-not-here-12345.txt"}, devNull(t), stdout, stderr)
	if err == nil {
		t.Fatal("expected error for missing file")
	} else if !strings.Contains(err.Error(), "cannot share") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMangledInviteIsError(t *testing.T) {
	stdout, stderr := devNullOut(t)
	err := run([]string{"tcthis-is-not-a-valid-address!!!"}, devNull(t), stdout, stderr)
	if err == nil {
		t.Fatal("expected error for mangled invite")
	} else if !strings.Contains(err.Error(), "invalid invite") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnknownFlagIsError(t *testing.T) {
	stdout, stderr := devNullOut(t)
	if err := run([]string{"--frobnicate"}, devNull(t), stdout, stderr); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

func TestHumanizeDuration(t *testing.T) {
	cases := map[string]string{
		"5s": "5 seconds", "1m": "1 minute", "10m": "10 minutes",
		"1h": "1 hour", "2h": "2 hours",
	}
	for in, want := range cases {
		d, err := time.ParseDuration(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := humanizeDuration(d); got != want {
			t.Fatalf("%s: got %q want %q", in, got, want)
		}
	}
}
