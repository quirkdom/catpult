package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
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

const testDERPURL = "https://relay.example.net/derpmap.json"

func TestBadDERPMapRejected(t *testing.T) {
	stdout, stderr := devNullOut(t)
	for _, bad := range []string{"", "not a url", "example.net/dm.json", "https://", "ftp://example.net/dm.json"} {
		if err := run([]string{"--derpmap", bad, "f.txt"}, devNull(t), stdout, stderr); err == nil {
			t.Fatalf("expected error for --derpmap %q", bad)
		}
	}
}

// TestDERPMapHTTPAllowed locks in tailcat parity: --derpmap imposes no
// https-only policy, so plain http map URLs parse fine (tailcat's own
// --derpmap-url accepts them; its default is https).
func TestDERPMapHTTPAllowed(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8080/derpmap.json", "https://relay.example.net/dm.json"} {
		if _, err := parseArgs([]string{"--derpmap", ok}); err != nil {
			t.Fatalf("expected %q to be accepted: %v", ok, err)
		}
	}
}

func TestDERPFlagParses(t *testing.T) {
	stdout, stderr := devNullOut(t)
	if err := run([]string{"--derp", "not a url", "f.txt"}, devNull(t), stdout, stderr); err == nil {
		t.Fatal("expected error for --derp URL")
	}

	opts, err := parseArgs([]string{"--derpmap", testDERPURL, "f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.derpmapSet || opts.derpmap != testDERPURL {
		t.Fatalf("space form: got derpmap %q derpmapSet %v", opts.derpmap, opts.derpmapSet)
	}
	opts, err = parseArgs([]string{"--derpmap=" + testDERPURL})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.derpmapSet || opts.derpmap != testDERPURL {
		t.Fatalf("equals form: got derpmap %q derpmapSet %v", opts.derpmap, opts.derpmapSet)
	}
}

func TestDERPEndpointsParse(t *testing.T) {
	opts, err := parseArgs([]string{"--derp", "relay-a.example.net, relay-b:8443 , [::1]:443 , ::1"})
	if err != nil {
		t.Fatal(err)
	}
	got := opts.derp
	want := []string{"relay-a.example.net", "relay-b:8443", "[::1]:443", "::1"}
	if !opts.derpSet || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("endpoints = %v, want %v", got, want)
	}
}

func TestBadDERPEndpointRejected(t *testing.T) {
	stdout, stderr := devNullOut(t)
	for _, bad := range []string{"", "relay-a,, relay-c", "https://relay.example.net:443/derp",
		"relay.example.net:0", "relay.example.net:99999", "relay:port", "host[name]", "rel ay.example.net"} {
		if err := run([]string{"--derp", bad, "f.txt"}, devNull(t), stdout, stderr); err == nil {
			t.Fatalf("expected error for --derp %q", bad)
		}
	}
}

func TestDERPAndDERPMapExclusive(t *testing.T) {
	stdout, stderr := devNullOut(t)
	if err := run([]string{"--derp", "relay.example.net", "--derpmap", testDERPURL, "f.txt"}, devNull(t), stdout, stderr); err == nil {
		t.Fatal("expected error for --derp together with --derpmap")
	}
}

func TestDERPWithReceiverIsError(t *testing.T) {
	stdout, stderr := devNullOut(t)
	invite := syntheticInvite(t)
	for _, args := range [][]string{
		{"--derp", "relay.example.net", invite},
		{"--derpmap", testDERPURL, invite},
	} {
		err := run(args, devNull(t), stdout, stderr)
		if err == nil {
			t.Fatalf("expected error for %v with receiver", args)
		} else if !strings.Contains(err.Error(), "sender options") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

// syntheticInvite returns a parseable tailcat address without touching the
// network, for tests of sender/receiver dispatch.
func syntheticInvite(t *testing.T) string {
	t.Helper()
	ci := tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()},
		PresharedKey:      tailcat.NewPresharedKey(),
		Region:            []*tailcfg.DERPRegion{{RegionID: 1}},
	}
	return string(ci.Addr())
}

func TestWatchFlagParses(t *testing.T) {
	opts, err := parseArgs([]string{"--watch", "f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.watch {
		t.Fatal("--watch did not set options.watch")
	}
	// The equals form is deliberately not supported.
	if _, err := parseArgs([]string{"--watch=true"}); err == nil {
		t.Fatal("expected unknown-flag error for --watch=true")
	}
}

func TestWatchWithReceiverIsError(t *testing.T) {
	stdout, stderr := devNullOut(t)
	invite := syntheticInvite(t)
	err := run([]string{"--watch", invite}, devNull(t), stdout, stderr)
	if err == nil {
		t.Fatal("expected error for --watch with receiver KEY")
	} else if !strings.Contains(err.Error(), "sender option") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWatchWithStdinIsError(t *testing.T) {
	stdout, stderr := devNullOut(t)
	for _, argv := range [][]string{
		{"--watch", "-"},
		{"--watch", "-", "a.txt"},
	} {
		if err := run(argv, devNull(t), stdout, stderr); err == nil {
			t.Fatalf("expected error for %v (files only)", argv)
		} else if !strings.Contains(err.Error(), "file arguments") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	// Piped stdin counts the same as `-`.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := run([]string{"--watch", "a.txt"}, r, stdout, stderr); err == nil {
		t.Fatal("expected error for --watch with piped stdin")
	}
}

func TestWatchWithNoFilesIsError(t *testing.T) {
	stdout, stderr := devNullOut(t)
	err := run([]string{"--watch"}, devNull(t), stdout, stderr)
	if err == nil {
		t.Fatal("expected error for --watch with no operands")
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
