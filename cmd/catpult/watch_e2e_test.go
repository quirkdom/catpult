package main

// End-to-end test of watch mode with two real catpult processes over the
// default Tailcat relay setup — no custom relay scaffolding, since relay
// configuration is orthogonal to watch semantics. What is asserted:
//
//   - the receiver gets the initial version over its one connection;
//   - the sender's file watcher notices an on-disk rewrite and re-sends
//     the new version over the same connection;
//   - SIGTERM on the sender closes the room and the receiver maps the
//     resulting EOF to "room closed." and prints the manifest twice.
//
// Requires external network (default relay); skipped under -short.
import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWatchE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("networked integration test")
	}
	bin := buildCatpult(t)

	// Sender: watch room hosted on the default relay.
	sendDir := t.TempDir()
	first := "A=1\n"
	if err := os.WriteFile(filepath.Join(sendDir, "watched.txt"), []byte(first), 0644); err != nil {
		t.Fatal(err)
	}
	senderCmd := exec.Command(bin, "--expires", "5m", "--watch", "watched.txt")
	senderCmd.Dir = sendDir
	senderStderr, err := senderCmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := senderCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		senderCmd.Process.Kill()
		senderCmd.Wait()
	}()
	invite := scanInvite(t, senderStderr)
	t.Logf("invite: %s (%d bytes)", invite[:20]+"...", len(invite))

	// Receiver: stays connected and rewrites the file in place per sent
	// version until the room closes.
	recvDir := t.TempDir()
	var out, errOut strings.Builder
	recvCmd := exec.Command(bin, invite, "--target", "got")
	recvCmd.Dir = recvDir
	recvCmd.Stdout = &out
	recvCmd.Stderr = &errOut
	if err := recvCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		recvCmd.Process.Kill()
		recvCmd.Wait()
	}()

	recvFile := filepath.Join(recvDir, "got", "watched.txt")
	waitForFileContent(t, recvFile, first, 60*time.Second)

	// Rewrite the watched file: the sender's poller notices and re-sends.
	second := "A=2 new content\n"
	if err := os.WriteFile(filepath.Join(sendDir, "watched.txt"), []byte(second), 0644); err != nil {
		t.Fatal(err)
	}
	waitForFileContent(t, recvFile, second, 60*time.Second)

	// End the room via the sender's own lifecycle (same as ^C).
	if err := senderCmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := senderCmd.Wait(); err != nil {
		t.Fatalf("sender failed: %v", err)
	}
	if err := recvCmd.Wait(); err != nil {
		t.Fatalf("receiver failed: %v\nstderr: %s", err, errOut.String())
	}

	// Manifest lists the file once per delivered version.
	if got, want := out.String(), "watched.txt\nwatched.txt\n"; got != want {
		t.Fatalf("stdout manifest = %q, want %q", got, want)
	}
	if !strings.Contains(errOut.String(), "room closed.") {
		t.Fatalf("receiver stderr missing \"room closed.\": %q", errOut.String())
	}
}

// waitForFileContent polls path until its contents equal want.
func waitForFileContent(t *testing.T, path, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, err := os.ReadFile(path)
		if err == nil && string(got) == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	got, _ := os.ReadFile(path)
	t.Fatalf("%s did not reach %q within %v (last: %q)", path, want, timeout, got)
}
