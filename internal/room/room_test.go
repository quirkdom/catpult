package room

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"catpult/internal/protocol"
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
	if _, err := Receive(client, target, &stdout, func(string, ...any) { warns++ }, nil, true); err != nil {
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
	if _, err := Receive(client, t.TempDir(), &stdout, nil, nil, false); err != nil {
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
	_, err := Receive(client, t.TempDir(), &stdout, nil, nil, false)
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
	go func() { _, err := Receive(c1, t.TempDir(), &out1, nil, nil, false); e1 <- err }()
	go func() { _, err := Receive(c2, t.TempDir(), &out2, nil, nil, false); e2 <- err }()

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
	_, err := Receive(client, t.TempDir(), &stdout, nil, nil, false)
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
	if _, err := Receive(client, t.TempDir(), &stdout, nil, nil, false); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if stdout.String() != "early byteslate" {
		t.Fatalf("late joiner stream = %q", stdout.String())
	}
}

func TestStreamIdleDoesNotTimeout(t *testing.T) {
	b := NewBroadcaster()
	client, server := net.Pipe()
	defer client.Close()
	go func() { _ = b.Serve(server) }()

	// Client starts with a very short deadline, which Receive sets for handshake.
	// Once ROOM_INFO is decoded, receiveStream must clear the read deadline so
	// an idle pause does not trigger a timeout error.
	go func() {
		time.Sleep(100 * time.Millisecond)
		b.Publish([]byte("hello after idle"))
		time.Sleep(50 * time.Millisecond)
		b.CloseAll()
	}()

	var stdout bytes.Buffer
	if _, err := Receive(client, t.TempDir(), &stdout, nil, nil, false); err != nil {
		t.Fatalf("receive failed during idle stream: %v", err)
	}
	if stdout.String() != "hello after idle" {
		t.Fatalf("got %q, want 'hello after idle'", stdout.String())
	}
}

func TestStreamCloseInputSendsEndToLiveSubs(t *testing.T) {
	b := NewBroadcaster()
	c1, s1 := net.Pipe()
	c2, s2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	go func() { _ = b.Serve(s1) }()
	go func() { _ = b.Serve(s2) }()

	b.Publish([]byte("stream-data-"))

	var out1, out2 bytes.Buffer
	e1 := make(chan error, 1)
	e2 := make(chan error, 1)
	go func() { _, err := Receive(c1, t.TempDir(), &out1, nil, nil, false); e1 <- err }()
	go func() { _, err := Receive(c2, t.TempDir(), &out2, nil, nil, false); e2 <- err }()

	time.Sleep(50 * time.Millisecond)
	b.Publish([]byte("part2"))
	time.Sleep(50 * time.Millisecond)
	b.CloseInput()

	if err := <-e1; err != ErrRoomClosed {
		t.Fatalf("sub1: expected ErrRoomClosed, got %v", err)
	}
	if err := <-e2; err != ErrRoomClosed {
		t.Fatalf("sub2: expected ErrRoomClosed, got %v", err)
	}
	if out1.String() != "stream-data-part2" {
		t.Fatalf("sub1 = %q", out1.String())
	}
	if out2.String() != "stream-data-part2" {
		t.Fatalf("sub2 = %q", out2.String())
	}
}

func TestStreamCloseInputLateJoinerGetsHistoryAndEnd(t *testing.T) {
	b := NewBroadcaster()
	b.Publish([]byte("completed "))
	b.Publish([]byte("stream"))
	b.CloseInput()

	// Subsequent publish after CloseInput should be ignored
	b.Publish([]byte("ignored"))

	client, server := net.Pipe()
	defer client.Close()
	go func() { _ = b.Serve(server) }()

	var stdout bytes.Buffer
	_, err := Receive(client, t.TempDir(), &stdout, nil, nil, false)
	if err != ErrRoomClosed {
		t.Fatalf("expected ErrRoomClosed, got %v", err)
	}
	if stdout.String() != "completed stream" {
		t.Fatalf("got %q, want 'completed stream'", stdout.String())
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

// waitForContent polls path until its contents equal want (or timeout),
// so tests can wait for each delivered watched-file version without
// racing the receiver's streaming write.
func waitForContent(t *testing.T, path string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := os.ReadFile(path)
		if err == nil && string(got) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s never reached %q: %v", path, want, err)
	}
	t.Fatalf("%s never reached %q; last content %q", path, want, got)
}

func TestFileWatcherBumpsVersionAndCoalesces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(p, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewFileWatcher([]string{p}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.Version(p); got != 1 {
		t.Fatalf("initial version = %d, want 1", got)
	}
	if got := w.Version(filepath.Join(dir, "other.txt")); got != 0 {
		t.Fatalf("unknown path version = %d, want 0", got)
	}

	var mu sync.Mutex
	var changed []string
	w.SetChangeCallback(func(name string) {
		mu.Lock()
		changed = append(changed, name)
		mu.Unlock()
	})
	w.Start()
	defer w.Close()

	sub := w.Subscribe()
	defer sub.Close()

	waitVer := func(want uint64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if w.Version(p) == want {
				return
			}
			select {
			case <-sub.Changes():
			case <-time.After(10 * time.Millisecond):
			}
		}
		t.Fatalf("version never reached %d (got %d)", want, w.Version(p))
	}
	waitVerAtLeast := func(wantAtLeast uint64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if w.Version(p) >= wantAtLeast {
				return
			}
			select {
			case <-sub.Changes():
			case <-time.After(10 * time.Millisecond):
			}
		}
		t.Fatalf("version never reached %d+ (got %d)", wantAtLeast, w.Version(p))
	}

	// Size change bumps the version.
	if err := os.WriteFile(p, []byte("v2 longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitVer(2)

	// Multiple rapid changes land between polls: each sampled poll bumps
	// the version at least once more (sampling may merge them into a
	// single bump — that is the coalescing behavior).
	if err := os.WriteFile(p, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("bb"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitVerAtLeast(3)

	mu.Lock()
	names := append([]string(nil), changed...)
	mu.Unlock()
	for _, n := range names {
		if n != "watched.txt" {
			t.Fatalf("change callback saw %q, want watched.txt", n)
		}
	}
	if len(names) == 0 {
		t.Fatal("change callback never fired")
	}
}

func TestWatchRoundTripOverPipe(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "watched.env")
	first := []byte("A=1\n")
	if err := os.WriteFile(src, first, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "inbox")

	w, err := NewFileWatcher([]string{src}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.Start()

	client, server := net.Pipe()
	defer client.Close()
	serveErr := make(chan error, 1)
	go func() { serveErr <- ServeWatchConn(server, w, []string{src}, nil) }()

	recvErr := make(chan error, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		_, err := Receive(client, target, &stdout, nil, func(format string, args ...any) {
			fmt.Fprintf(&stderr, format, args...)
		}, false)
		recvErr <- err
	}()

	gotFile := filepath.Join(target, "watched.env")
	waitForContent(t, gotFile, string(first), 5*time.Second)

	// Rewrite the file: the receiver should end up holding the new version.
	second := "A=22\nB=3\n"
	if err := os.WriteFile(src, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForContent(t, gotFile, second, 5*time.Second)

	// Close the room first: the watch handler closes conn, which the
	// receiver maps to ErrRoomClosed, not a completed transfer. (The
	// EOF-from-gone-path mapping is covered by the supersede test.)
	w.Close()
	if err := <-recvErr; err != ErrRoomClosed {
		t.Fatalf("receive: got %v, want ErrRoomClosed", err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("serve: %v", err)
	}

	// In watch mode, the manifest prints the filename once (not per update).
	if got, want := stdout.String(), "watched.env\n"; got != want {
		t.Fatalf("stdout manifest = %q, want %q", got, want)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("received updated watched.env")) {
		t.Fatalf("stderr missing update note: %q", stderr.String())
	}
}

// TestWatchSupersedeMidFile feeds the receiver header + partial chunks and
// then a second header for the same file, exercising the implicit
// in-flight supersede: the partial is discarded, the new version lands
// complete, and the manifest lists the filename only once for it.
func TestWatchSupersedeMidFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "inbox")

	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		_ = protocol.WriteFrame(server, protocol.MsgRoomInfo, protocol.EncodeRoomInfo(protocol.PayloadFileWatch))
		h1, _ := protocol.EncodeFileHeader("f.txt", 100)
		_ = protocol.WriteFrame(server, protocol.MsgFileHeader, h1)
		_ = protocol.WriteFrame(server, protocol.MsgFileChunk, bytes.Repeat([]byte("x"), 40))
		// Supersede mid-file: fresh header discards the 40-byte partial.
		h2, _ := protocol.EncodeFileHeader("f.txt", 5)
		_ = protocol.WriteFrame(server, protocol.MsgFileHeader, h2)
		_ = protocol.WriteFrame(server, protocol.MsgFileChunk, []byte("hello"))
		_ = protocol.WriteFrame(server, protocol.MsgFileEnd, nil)
		_ = protocol.WriteFrame(server, protocol.MsgWatchInitialEnd, nil)
		// A full version of a second file confirms the connection stays open.
		h3, _ := protocol.EncodeFileHeader("g.txt", 2)
		_ = protocol.WriteFrame(server, protocol.MsgFileHeader, h3)
		_ = protocol.WriteFrame(server, protocol.MsgFileChunk, []byte("ok"))
		_ = protocol.WriteFrame(server, protocol.MsgFileEnd, nil)
	}()

	var stdout bytes.Buffer
	_, err := Receive(client, target, &stdout, nil, nil, true)
	if err != ErrRoomClosed {
		t.Fatalf("receive: got %v, want ErrRoomClosed", err)
	}
	if got, want := stdout.String(), "f.txt\ng.txt\n"; got != want {
		t.Fatalf("stdout manifest = %q, want %q", got, want)
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	got, err := root.ReadFile("f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("f.txt = %q, want %q", got, "hello")
	}
	// The 40-byte partial of the superseded version left nothing behind:
	// the target dir holds exactly the completed files.
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"f.txt", "g.txt"}) {
		t.Fatalf("target dir = %v, want exactly f.txt g.txt", names)
	}
}

func TestServeWatchConnDisconnectsPromptly(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(src, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewFileWatcher([]string{src}, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	defer w.Close()

	client, server := net.Pipe()
	serveErr := make(chan error, 1)
	go func() { serveErr <- ServeWatchConn(server, w, []string{src}, nil) }()

	// Client reads ROOM_INFO
	fr, err := protocol.ReadFrame(client, protocol.MaxHeader)
	if err != nil {
		t.Fatal(err)
	}
	if fr.Type != protocol.MsgRoomInfo {
		t.Fatalf("expected ROOM_INFO, got %v", fr.Type)
	}
	// Drain the initial file
	for {
		f, err := protocol.ReadFrame(client, protocol.MaxFrame)
		if err != nil {
			t.Fatal(err)
		}
		if f.Type == protocol.MsgWatchInitialEnd {
			break
		}
	}

	// Now recipient disconnects
	client.Close()

	select {
	case err := <-serveErr:
		if err == nil {
			t.Fatal("expected disconnect error, got nil")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("ServeWatchConn failed to detect client disconnect promptly")
	}
}

func TestWatchReceiverInitialReceiveWaitingLog(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "init.txt")
	if err := os.WriteFile(src, []byte("hello initial"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewFileWatcher([]string{src}, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.Start()
	defer w.Close()

	client, server := net.Pipe()
	defer client.Close()
	go func() { _ = ServeWatchConn(server, w, []string{src}, nil) }()

	target := filepath.Join(dir, "target")
	var stdout, stderr bytes.Buffer
	recvErr := make(chan error, 1)
	go func() {
		_, err := Receive(client, target, &stdout, nil, func(format string, args ...any) {
			fmt.Fprintf(&stderr, format, args...)
		}, false)
		recvErr <- err
	}()

	// Wait for target file to be written
	waitForContent(t, filepath.Join(target, "init.txt"), "hello initial", 5*time.Second)

	// Within 500ms, the receiver should announce completion of initial receive
	time.Sleep(150 * time.Millisecond)

	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "received 1 file(s). waiting for updates...") {
		t.Fatalf("expected 'waiting for updates...' note, got: %q", stderrStr)
	}
	if strings.Contains(stderrStr, "received updated") {
		t.Fatalf("initial receive should not contain 'received updated', got: %q", stderrStr)
	}

	w.Close()
	_ = <-recvErr
}

func TestWatchInitialBatchLogWaitsForMarker(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	target := filepath.Join(t.TempDir(), "target")
	var stderr bytes.Buffer
	recvErr := make(chan error, 1)
	go func() {
		_, err := Receive(client, target, io.Discard, nil, func(format string, args ...any) {
			fmt.Fprintf(&stderr, format, args...)
		}, false)
		recvErr <- err
	}()

	if err := protocol.WriteFrame(server, protocol.MsgRoomInfo, protocol.EncodeRoomInfo(protocol.PayloadFileWatch)); err != nil {
		t.Fatal(err)
	}
	header, _ := protocol.EncodeFileHeader("slow.txt", 1)
	for _, frame := range []struct {
		typ     protocol.MsgType
		payload []byte
	}{
		{protocol.MsgFileHeader, header},
		{protocol.MsgFileChunk, []byte("x")},
		{protocol.MsgFileEnd, nil},
	} {
		if err := protocol.WriteFrame(server, frame.typ, frame.payload); err != nil {
			t.Fatal(err)
		}
	}

	time.Sleep(100 * time.Millisecond)
	if got := stderr.String(); strings.Contains(got, "waiting for updates") {
		t.Fatalf("receiver announced initial completion before marker: %q", got)
	}
	if err := protocol.WriteFrame(server, protocol.MsgWatchInitialEnd, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(stderr.String(), "waiting for updates") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "received 1 file(s). waiting for updates...") {
		t.Fatalf("receiver did not announce after marker: %q", stderr.String())
	}
	_ = server.Close()
	if err := <-recvErr; err != ErrRoomClosed {
		t.Fatalf("receive: got %v, want ErrRoomClosed", err)
	}
}
