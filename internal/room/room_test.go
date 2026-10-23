package room

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateFilenameRejects(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{
		"", "/abs/path.txt", "../escape.txt", "a/../../x", "..",
		strings.Repeat("a", 256), "nul\x00byte",
	} {
		if _, err := ValidateFilename(bad, dir); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
	}
}

func TestValidateFilenameConfines(t *testing.T) {
	dir := t.TempDir()
	dest, err := ValidateFilename("sub/../ok.txt", dir)
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(dir)
	if !strings.HasPrefix(dest, abs+string(os.PathSeparator)) && dest != abs {
		t.Fatalf("dest escapes target: %q", dest)
	}
}

func TestFileRoundTripOverPipe(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.env")
	want := []byte("SECRET=abc123\n")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "inbox")

	client, server := net.Pipe()
	defer client.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- ServeFileConn(server, []string{src}, nil) }()

	var stdout bytes.Buffer
	warns := 0
	if err := Receive(client, target, &stdout, func(string, ...any) { warns++ }, true); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != "src.env" {
		t.Fatalf("stdout manifest = %q", stdout.String())
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	got, err := root.ReadFile("src.env")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content mismatch: %q", got)
	}
	if warns != 0 {
		t.Fatalf("unexpected warnings for tty file receive: %d", warns)
	}
}

func TestStreamRoundTripOverPipe(t *testing.T) {
	b := NewBroadcaster()
	client, server := net.Pipe()
	defer client.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- b.Serve(server) }()
	go func() {
		b.Publish([]byte("hello "))
		b.Publish([]byte("stream"))
		time.Sleep(50 * time.Millisecond)
		b.CloseAll()
	}()

	var stdout bytes.Buffer
	if err := Receive(client, t.TempDir(), &stdout, func(string, ...any) {}, false); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if stdout.String() != "hello stream" {
		t.Fatalf("stream = %q", stdout.String())
	}
	<-serveErr
}

func TestStreamShutdownSendsEnd(t *testing.T) {
	b := NewBroadcaster()
	client, server := net.Pipe()
	defer client.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- b.Serve(server) }()
	go func() {
		b.Publish([]byte("before "))
		time.Sleep(50 * time.Millisecond)
		b.Shutdown()
	}()

	var stdout bytes.Buffer
	err := Receive(client, t.TempDir(), &stdout, func(string, ...any) {}, false)
	if err != ErrRoomClosed {
		t.Fatalf("expected ErrRoomClosed, got %v", err)
	}
	if stdout.String() != "before " {
		t.Fatalf("stream = %q", stdout.String())
	}
	<-serveErr
}

func TestStreamShutdownSendsEndWithMultipleSubs(t *testing.T) {
	b := NewBroadcaster()
	c1, s1 := net.Pipe()
	c2, s2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	go func() { _ = b.Serve(s1) }()
	go func() { _ = b.Serve(s2) }()
	b.Publish([]byte("shared"))

	var out1, out2 bytes.Buffer
	e1 := make(chan error, 1)
	e2 := make(chan error, 1)
	go func() { e1 <- Receive(c1, t.TempDir(), &out1, func(string, ...any) {}, false) }()
	go func() { e2 <- Receive(c2, t.TempDir(), &out2, func(string, ...any) {}, false) }()

	time.Sleep(50 * time.Millisecond)
	b.Shutdown()

	if err := <-e1; err != ErrRoomClosed {
		t.Fatalf("sub1: expected ErrRoomClosed, got %v", err)
	}
	if err := <-e2; err != ErrRoomClosed {
		t.Fatalf("sub2: expected ErrRoomClosed, got %v", err)
	}
	if out1.String() != "shared" {
		t.Fatalf("sub1 stream = %q", out1.String())
	}
	if out2.String() != "shared" {
		t.Fatalf("sub2 stream = %q", out2.String())
	}
}

func TestStreamCloseAllDoesNotSendEnd(t *testing.T) {
	b := NewBroadcaster()
	client, server := net.Pipe()
	defer client.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- b.Serve(server) }()
	go func() {
		b.Publish([]byte("before "))
		time.Sleep(50 * time.Millisecond)
		b.CloseAll()
	}()

	var stdout bytes.Buffer
	err := Receive(client, t.TempDir(), &stdout, func(string, ...any) {}, false)
	// CloseAll force-closes: receiver sees EOF (not MsgStreamEnd).
	// Either nil (EOF on pipe) or an error is acceptable — the key is
	// it does NOT return ErrRoomClosed (no STREAM_END was sent).
	if err == ErrRoomClosed {
		t.Fatalf("CloseAll should not send STREAM_END")
	}
	<-serveErr
}

func TestStreamLateJoinerGetsHistory(t *testing.T) {
	b := NewBroadcaster()
	b.Publish([]byte("early "))
	b.Publish([]byte("bytes"))

	client, server := net.Pipe()
	defer client.Close()
	go func() { _ = b.Serve(server) }()
	go func() {
		time.Sleep(50 * time.Millisecond)
		b.Publish([]byte("late"))
		time.Sleep(50 * time.Millisecond)
		b.CloseAll()
	}()

	var stdout bytes.Buffer
	if err := Receive(client, t.TempDir(), &stdout, func(string, ...any) {}, false); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if stdout.String() != "early byteslate" {
		t.Fatalf("late joiner stream = %q", stdout.String())
	}
}

func TestMaliciousFilenameRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := ValidateFilename("../../etc/passwd", dir); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := ValidateFilename("/etc/passwd", dir); err == nil {
		t.Fatal("expected absolute rejection")
	}
}
