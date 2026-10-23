package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"catpult/internal/protocol"
	"catpult/internal/room"
	"catpult/internal/transport"
)

var version = "dev"

const defaultExpires = 30 * time.Minute

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "catpult: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	expires    time.Duration
	expiresSet bool
	target     string
	targetSet  bool
	operands   []string
}

func run(argv []string, stdin *os.File, stdout, stderr *os.File) error {
	opts, err := parseArgs(argv)
	if err != nil {
		return err
	}
	if opts == nil {
		return nil // --help / --version already printed
	}

	stdinPiped := !isCharDevice(stdin)
	isStdoutTTY := term.IsTerminal(int(stdout.Fd()))

	// Receiver: a single KEY with no file operands.
	if len(opts.operands) == 1 && transport.IsKey(opts.operands[0]) {
		if opts.expiresSet {
			return fmt.Errorf("--expires is a sender option; receivers exit on room closure, ^C, or transfer completion")
		}
		if stdinPiped {
			return fmt.Errorf("receiver never consumes stdin: `something | catpult KEY` is invalid")
		}
		target := opts.target
		if target == "" {
			target = "."
		}
		return runReceiver(opts.operands[0], target, stdout, stderr, isStdoutTTY)
	}
	if opts.targetSet {
		return fmt.Errorf("--target is a receiver option: `catpult KEY [--target DIR]`")
	}
	// Helpful error for a mangled invite rather than "file not found".
	if len(opts.operands) == 1 && strings.HasPrefix(opts.operands[0], "tc") {
		if _, statErr := os.Stat(opts.operands[0]); os.IsNotExist(statErr) {
			return fmt.Errorf("invalid invite %q: not a parseable Tailcat address", opts.operands[0])
		}
	}

	hasHyphen := slices.Contains(opts.operands, "-")
	if hasHyphen && len(opts.operands) > 1 {
		return fmt.Errorf("`-` (stdin) cannot be combined with file arguments")
	}
	if stdinPiped && len(opts.operands) > 0 {
		return fmt.Errorf("file arguments and piped stdin are mutually exclusive: `producer | catpult FILE` is invalid")
	}

	expires := defaultExpires
	if opts.expiresSet {
		expires = opts.expires
	}

	switch {
	case hasHyphen || stdinPiped:
		return runSenderStream(expires, stdin, stderr)
	case len(opts.operands) == 0:
		usage(stderr)
		return fmt.Errorf("no files specified; usage: catpult [--expires D] FILE... | catpult KEY [--target DIR]")
	default:
		return runSenderFiles(expires, opts.operands, stderr)
	}
}

func parseArgs(argv []string) (*options, error) {
	opts := &options{}
	for i := 0; i < len(argv); {
		a := argv[i]
		switch {
		case a == "--":
			opts.operands = append(opts.operands, argv[i+1:]...)
			i = len(argv)
		case a == "-h" || a == "--help" || a == "help":
			usage(os.Stdout)
			return nil, nil
		case a == "-v" || a == "--version" || a == "version":
			fmt.Printf("catpult %s\n", version)
			return nil, nil
		case a == "--expires":
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("flag %q requires a value", a)
			}
			d, err := time.ParseDuration(argv[i+1])
			if err != nil || d < 0 {
				return nil, fmt.Errorf("invalid --expires %q: must be a non-negative Go duration (e.g. 10m, 1h)", argv[i+1])
			}
			opts.expires = d
			opts.expiresSet = true
			i += 2
		case a == "--target":
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("flag %q requires a value", a)
			}
			opts.target = argv[i+1]
			opts.targetSet = true
			i += 2
		case strings.HasPrefix(a, "--expires="):
			d, err := time.ParseDuration(strings.TrimPrefix(a, "--expires="))
			if err != nil || d < 0 {
				return nil, fmt.Errorf("invalid --expires %q: must be a non-negative Go duration (e.g. 10m, 1h)", a)
			}
			opts.expires = d
			opts.expiresSet = true
			i++
		case strings.HasPrefix(a, "--target="):
			opts.target = strings.TrimPrefix(a, "--target=")
			opts.targetSet = true
			i++
		case strings.HasPrefix(a, "-") && a != "-":
			return nil, fmt.Errorf("unknown flag %q", a)
		default:
			opts.operands = append(opts.operands, a)
			i++
		}
	}
	return opts, nil
}

// --- sender: files ---

func runSenderFiles(expires time.Duration, files []string, stderr *os.File) error {
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			return fmt.Errorf("cannot share %q: %w", f, err)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("cannot share %q: not a regular file", f)
		}
	}

	var mu sync.Mutex
	conns := make(map[net.Conn]struct{})
	var served atomic.Int64

	handler := func(conn net.Conn) {
		mu.Lock()
		conns[conn] = struct{}{}
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(conns, conn)
			mu.Unlock()
		}()
		n := served.Add(1)
		fmt.Fprintf(stderr, "recipient %d connected.\n", n)
		err := room.ServeFileConn(conn, files, func(name string, size int64) {
			fmt.Fprintf(stderr, "Sending %s (%d bytes)...\n", name, size)
		})
		if err != nil {
			fmt.Fprintf(stderr, "recipient %d: %v\n", n, err)
			return
		}
		fmt.Fprintf(stderr, "sent %d file(s) to recipient %d.\n", len(files), n)
	}

	srv, addr, err := transport.NewServer(handler)
	if err != nil {
		return err
	}
	printInvite(stderr, string(addr), expires)

	waitDone := waitForShutdown(expires)

	mu.Lock()
	for c := range conns {
		c.Close()
	}
	mu.Unlock()
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.DrainTCP(drainCtx)
	if err := srv.Close(); err != nil {
		fmt.Fprintf(stderr, "closing room: %v\n", err)
	}
	fmt.Fprintf(stderr, "room closed (%s).\n", waitDone)
	return nil
}

// --- sender: stream ---

func runSenderStream(expires time.Duration, stdin, stderr *os.File) error {
	bc := room.NewBroadcaster()
	handler := func(conn net.Conn) {
		n := bc.Count() + 1
		if err := bc.Serve(conn); err != nil {
			fmt.Fprintf(stderr, "recipient %d: %v\n", n, err)
		}
	}
	srv, addr, err := transport.NewServer(handler)
	if err != nil {
		return err
	}
	printInvite(stderr, string(addr), expires)
	fmt.Fprintf(stderr, "Reading stream from stdin (EOF does not close the room).\n")

	done := make(chan struct{})
	go func() {
		buf := make([]byte, protocol.ChunkSize)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, rerr := stdin.Read(buf)
			if n > 0 {
				select {
				case <-done:
					return
				default:
					bc.Publish(buf[:n])
				}
			}
			if rerr != nil {
				if rerr != io.EOF {
					fmt.Fprintf(stderr, "reading stdin: %v\n", rerr)
				} else {
					fmt.Fprintf(stderr, "stdin closed; room stays open for late joiners until expiry or ^C.\n")
				}
				return
			}
		}
	}()

	reason := waitForShutdown(expires)

	// os.Stdin.Close() does not reliably unblock a blocking Read on macOS or Linux.
	// Temporarily ignore SIGINT so the kill doesn't terminate the process
	// after waitForShutdown unregistered the handler.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, shutdownSignals()...)
	defer signal.Stop(sigCh)
	close(done)
	_ = selfInterrupt()
	<-sigCh // drain the delivered signal
	bc.Shutdown()
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.DrainTCP(drainCtx)
	if err := srv.Close(); err != nil {
		fmt.Fprintf(stderr, "closing room: %v\n", err)
	}
	fmt.Fprintf(stderr, "room closed (%s).\n", reason)
	return nil
}

// --- receiver ---

func runReceiver(key, target string, stdout, stderr *os.File, isStdoutTTY bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), transport.DialTimeout)
	defer cancel()
	conn, cl, err := transport.Dial(ctx, key)
	if err != nil {
		return err
	}
	defer cl.Close()
	warnf := func(format string, args ...any) { fmt.Fprintf(stderr, format, args...) }
	if err := room.Receive(conn, target, stdout, warnf, isStdoutTTY); err != nil {
		conn.Close()
		if errors.Is(err, room.ErrRoomClosed) {
			fmt.Fprintf(stderr, "room closed.\n")
			return nil
		}
		return err
	}
	conn.Close()
	drainCtx, dcancel := context.WithTimeout(context.Background(), time.Second)
	defer dcancel()
	_ = cl.DrainTCP(drainCtx)
	fmt.Fprintf(stderr, "transfer completed.\n")
	return nil
}

// --- lifecycle helpers ---

// waitForShutdown blocks until an interrupt/terminate signal or expiry,
// returning a short reason string. expires <= 0 means "until ^C".
func waitForShutdown(expires time.Duration) string {
	sigCtx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	if expires <= 0 {
		<-sigCtx.Done()
		return "SIGTERM"
	}
	timer := time.NewTimer(expires)
	defer timer.Stop()
	select {
	case <-sigCtx.Done():
		return "SIGTERM"
	case <-timer.C:
		return "expired"
	}
}

func printInvite(stderr *os.File, invite string, expires time.Duration) {
	exp := "until ^C (no expiry)"
	if expires > 0 {
		exp = "in " + humanizeDuration(expires)
	}
	fmt.Fprintf(stderr, "Catpult room created.\n\nExpires: %s\n\nInvite:\n%s\n\nShare this invite with recipients out of band.\n\nWaiting for recipients...\n", exp, invite)
}

func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		s := int(d.Seconds())
		if s <= 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", s)
	}
	if d < time.Hour {
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	}
	h := int(d.Hours())
	if h == 1 {
		return "1 hour"
	}
	return fmt.Sprintf("%d hours", h)
}

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return true
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func usage(w *os.File) {
	fmt.Fprint(w, `catpult - share secrets securely over tailnet (no Catpult server)

Sender (files):
  catpult [--expires DURATION] FILE...

Sender (stdin stream):
  catpult [--expires DURATION] -
  producer | catpult [--expires DURATION]

Receiver:
  catpult KEY [--target DIR]

Flags:
  --expires D   room lifetime (Go duration, e.g. 10m, 1h); default 30m; 0 = until ^C
  --target DIR  receive directory (default: current directory)
  -h, --help    show this help
  -v, --version print version

stdout carries payload output only (filenames in file mode, raw bytes in
stream mode); all status, warnings, and errors go to stderr.
Delivery is best effort: "sent" / "transfer completed", never "delivered".
The invite is a secret bearer capability — share it out of band.
`)
}
