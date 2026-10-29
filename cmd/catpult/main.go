package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
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
	noExpiry   bool
	target     string
	targetSet  bool
	watch      bool     // sender: keep the room open and re-send files on change
	derp       []string // validated self-hosted relay endpoints (host[:port])
	derpSet    bool
	derpmap    string // URL of a JSON DERP relay map
	derpmapSet bool
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
		if opts.noExpiry {
			return fmt.Errorf("--no-expiry is a sender option; receivers exit on room closure, ^C, or transfer completion")
		}
		if opts.derpSet || opts.derpmapSet {
			return fmt.Errorf("--derp and --derpmap are sender options; relay details are embedded in the invite, so receivers need no relay configuration")
		}
		if opts.watch {
			return fmt.Errorf("--watch is a sender option; the receiver keeps receiving until the room closes regardless")
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
	if opts.watch && (hasHyphen || stdinPiped) {
		return fmt.Errorf("--watch requires file arguments: watch mode re-sends named files on change, it cannot watch a stdin stream")
	}

	expires := defaultExpires
	if opts.expiresSet {
		expires = opts.expires
	} else if opts.noExpiry {
		expires = 0
	}

	relay := transport.Relay{}
	if opts.derpSet {
		relay.Endpoints = opts.derp
	} else if opts.derpmapSet {
		relay.MapURL = opts.derpmap
	}

	switch {
	case hasHyphen || stdinPiped:
		return runSenderStream(expires, relay, stdin, stderr)
	case len(opts.operands) == 0:
		usage(stderr)
		return fmt.Errorf("no files specified; usage: catpult [--expires D | --no-expiry] [--derp H[,H...]] [--derpmap URL] FILE... | catpult KEY [--target DIR]")
	default:
		return runFileSender(expires, relay, opts.operands, stderr, opts.watch)
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
		case a == "--no-expiry":
			// Boolean flag; only the bare form is accepted. `--no-expiry=<x>`
			// falls through to the unknown-flag error below.
			opts.noExpiry = true
			opts.expires = 0
			i++
		case a == "--watch":
			// Boolean flag; only the bare form is accepted. `--watch=<x>`
			// falls through to the unknown-flag error below.
			opts.watch = true
			i++
		case a == "--derp":
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("flag %q requires a value", a)
			}
			ends, err := parseDERPEndpoints(argv[i+1])
			if err != nil {
				return nil, err
			}
			opts.derp = ends
			opts.derpSet = true
			i += 2
		case strings.HasPrefix(a, "--derp="):
			ends, err := parseDERPEndpoints(strings.TrimPrefix(a, "--derp="))
			if err != nil {
				return nil, err
			}
			opts.derp = ends
			opts.derpSet = true
			i++
		case a == "--derpmap":
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("flag %q requires a value", a)
			}
			if err := validateDERPMapURL(argv[i+1]); err != nil {
				return nil, err
			}
			opts.derpmap = argv[i+1]
			opts.derpmapSet = true
			i += 2
		case strings.HasPrefix(a, "--derpmap="):
			v := strings.TrimPrefix(a, "--derpmap=")
			if err := validateDERPMapURL(v); err != nil {
				return nil, err
			}
			opts.derpmap = v
			opts.derpmapSet = true
			i++
		case strings.HasPrefix(a, "-") && a != "-":
			return nil, fmt.Errorf("unknown flag %q", a)
		default:
			opts.operands = append(opts.operands, a)
			i++
		}
	}
	if opts.expiresSet && opts.noExpiry {
		return nil, fmt.Errorf("--no-expiry and --expires are mutually exclusive")
	}
	if opts.derpSet && opts.derpmapSet {
		return nil, fmt.Errorf("--derp and --derpmap are mutually exclusive; give relay endpoints or a DERP map URL, not both")
	}
	return opts, nil
}

// parseDERPEndpoints validates a comma-separated --derp value via
// transport.ParseRelayEndpoints, wrapping errors in flag context.
func parseDERPEndpoints(spec string) ([]string, error) {
	ends, err := transport.ParseRelayEndpoints(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid --derp %q: %v", spec, err)
	}
	return ends, nil
}

// validateDERPMapURL rejects obviously malformed DERP map URLs early, so the
// failure surfaces as a flag error rather than a network error deep inside
// Tailcat's DERP map fetch. Like tailcat's --derpmap-url, it imposes no
// https-only policy: plain http is accepted (Tailcat's own default is https,
// and the map is trusted infrastructure config).
func validateDERPMapURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid --derpmap %q: must be an absolute URL to a JSON DERP map (e.g. https://example.net/derpmap.json)", s)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid --derpmap %q: unsupported scheme %q (want http or https)", s, u.Scheme)
	}
	return nil
}

// --- sender: files ---

// runFileSender hosts a file room: a finite one-shot transfer (watch=false)
// or a watch room that re-sends watched files to every connected recipient
// whenever they change on disk (watch=true). Shared lifecycle: handler
// bookkeeping (recipient numbering, active-connection registry), invite
// printing, and shutdown — active connections are closed before waiting
// on handlers, so a stalled recipient cannot hang teardown.
func runFileSender(expires time.Duration, relay transport.Relay, files []string, stderr *os.File, watch bool) error {
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			return fmt.Errorf("cannot share %q: %w", f, err)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("cannot share %q: not a regular file", f)
		}
	}

	var watcher *room.FileWatcher
	if watch {
		var err error
		watcher, err = room.NewFileWatcher(files, room.WatchPollInterval)
		if err != nil {
			return err
		}
	}

	var mu sync.Mutex
	conns := make(map[net.Conn]struct{})
	var served atomic.Int64
	// Handlers run for the life of their connection (the whole room in
	// watch mode; until this recipient's transfer completes in file mode),
	// so shutdown waits on them after closing the conns.
	var wgHandlers sync.WaitGroup

	serve := func(conn net.Conn) error {
		if watch {
			return room.ServeWatchConn(conn, watcher, files, func(ev room.WatchEvent) {
				switch ev.Kind {
				case room.WatchSending:
					fmt.Fprintf(stderr, "Sending %s (%d bytes)...\n", ev.Name, ev.Size)
				case room.WatchMissing:
					fmt.Fprintf(stderr, "watched file %s is missing; skipping until it reappears\n", ev.Name)
				}
			})
		}
		return room.ServeFileConn(conn, files, func(name string, size int64) {
			fmt.Fprintf(stderr, "Sending %s (%d bytes)...\n", name, size)
		})
	}

	handler := func(conn net.Conn) {
		wgHandlers.Add(1)
		defer wgHandlers.Done()
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
		if err := serve(conn); err != nil {
			fmt.Fprintf(stderr, "recipient %d: %v\n", n, err)
			return
		}
		// No completion line in watch mode: the connection stays open for
		// the next version until the room closes.
		if !watch {
			fmt.Fprintf(stderr, "sent %d file(s) to recipient %d.\n", len(files), n)
		}
	}

	srv, addr, err := transport.NewServer(handler, relay)
	if err != nil {
		return err
	}
	printInvite(stderr, string(addr), expires, relayDescription(relay))
	if watch {
		recipientCount := func() int {
			mu.Lock()
			defer mu.Unlock()
			return len(conns)
		}
		// One line per detected change, regardless of how many recipients
		// are watching (each handler supersedes its own in-flight transfer).
		watcher.SetChangeCallback(func(name string) {
			fmt.Fprintf(stderr, "%s changed; resending to %d recipient(s).\n", name, recipientCount())
		})
		watcher.SetWarnCallback(func(format string, args ...any) {
			fmt.Fprintf(stderr, format, args...)
		})
		watcher.Start()
		base := make([]string, len(files))
		for i, f := range files {
			base[i] = filepath.Base(f)
		}
		fmt.Fprintf(stderr, "Watch mode: re-sending %s whenever it/they change on disk. Recipients stay connected until the room closes.\n",
			strings.Join(base, ", "))
	}

	waitDone := waitForShutdown(expires)

	// Shutdown order: stop the watcher first so handlers' closing() gates
	// flip and no new resend pass starts; then close every active
	// connection so in-flight writes fail immediately instead of stalling
	// teardown; then wait for handlers (each closes its own conn as it
	// exits); finally drain/close the transport.
	if watcher != nil {
		watcher.Close()
	}
	mu.Lock()
	for c := range conns {
		c.Close()
	}
	mu.Unlock()
	wgHandlers.Wait()
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

func runSenderStream(expires time.Duration, relay transport.Relay, stdin, stderr *os.File) error {
	bc := room.NewBroadcaster()
	handler := func(conn net.Conn) {
		n := bc.Count() + 1
		if err := bc.Serve(conn); err != nil {
			fmt.Fprintf(stderr, "recipient %d: %v\n", n, err)
		}
	}
	srv, addr, err := transport.NewServer(handler, relay)
	if err != nil {
		return err
	}
	printInvite(stderr, string(addr), expires, relayDescription(relay))
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
				bc.CloseInput()
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

// relayDescription renders a one-line description of the room's custom
// relay for the invite block; empty means the default relay. (The two
// mechanisms are mutually exclusive, enforced for parse on both sides.)
func relayDescription(relay transport.Relay) string {
	if len(relay.Endpoints) > 0 {
		return strings.Join(relay.Endpoints, ", ") + " (self-hosted DERP)"
	}
	if relay.MapURL != "" {
		return relay.MapURL + " (custom DERP map)"
	}
	return ""
}

func printInvite(stderr *os.File, invite string, expires time.Duration, relayDesc string) {
	exp := "until ^C (no expiry)"
	if expires > 0 {
		exp = "in " + humanizeDuration(expires)
	}
	fmt.Fprintf(stderr, "Catpult room created.\n")
	if relayDesc != "" {
		fmt.Fprintf(stderr, "\nRelay: %s\n", relayDesc)
	}
	fmt.Fprintf(stderr, "\nExpires: %s\n\nInvite:\n%s\n\nShare this invite with recipients out of band.\n\nWaiting for recipients...\n", exp, invite)
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
  catpult [--expires DURATION | --no-expiry] [--derp H[,H...]] [--derpmap URL] FILE...

Sender (files, watch mode):
  catpult --watch [--expires DURATION | --no-expiry] FILE...
  the room stays open and each FILE is re-sent to every connected
  recipient whenever it changes on disk (mtime+size polling, ~1s);
  a version in flight when the file changes is dropped and the new
  version re-sends from the start

Sender (stdin stream):
  catpult [--expires DURATION | --no-expiry] [--derp H[,H...]] [--derpmap URL] -
  producer | catpult [--expires DURATION | --no-expiry]

Receiver:
  catpult KEY [--target DIR]

Flags:
  --expires D   room lifetime (Go duration, e.g. 10m, 1h); default 30m; 0 = until ^C
  --no-expiry   disable room expiry (equivalent to --expires=0; until ^C)
  --target DIR  receive directory (default: current directory)
  --derp HOSTS  use self-hosted DERP relay endpoint(s) for senders:
                comma-separated host[:port] list (or [v6host]:port), TLS,
                default port 443; when set no DERP map is fetched
  --watch       file sender: keep the room open and re-send each FILE on
                change; files only (no "-" or stdin); default expiry stays
                30m; the receiver must also support watch mode
  --derpmap URL fetch the DERP relay map from URL instead of the default
                (alternative to --derp; sender option; the invite embeds
                the chosen relay, so recipients need no relay config).
                http and https are both accepted, as in tailcat
  -h, --help    show this help
  -v, --version print version

stdout carries payload output only (filenames in file mode, raw bytes in
stream mode); all status, warnings, and errors go to stderr.
Delivery is best effort: "sent" / "transfer completed", never "delivered".
The invite is a secret bearer capability — share it out of band.
`)
}
