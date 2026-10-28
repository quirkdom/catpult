package room

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"catpult/internal/protocol"
)

// WatchPollInterval is how often the watcher re-stats each watched path.
// Detection latency is therefore <= one interval; no extra dependency and
// cross-platform. stat runs against the path each tick (never an open
// handle), so editors that save via atomic rename are seen correctly.
const WatchPollInterval = 1 * time.Second

// fileSig is the change-detection signature of a watched path: mtime + size.
// A touch with unchanged contents is treated as a change (resend; content
// hashing is not part of watch mode).
type fileSig struct {
	modNS int64
	size  int64
}

func statSig(path string) (fileSig, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileSig{}, err
	}
	return fileSig{modNS: fi.ModTime().UnixNano(), size: fi.Size()}, nil
}

// FileWatcher polls watched paths and bumps a per-path version on any
// change (mtime or size differs from the last signature). It is the shared
// change source for every recipient connection's watch handler.
//
// A missing/deleted watched file is reported once via the warn callback
// (if installed) and skipped, not fatal: it may reappear via an atomic
// save, and its version is preserved until it does. The watcher never
// follows new paths — only the FILE...
// set given at room creation is watched.
type FileWatcher struct {
	interval time.Duration
	onChange func(name string)                // optional, invoked outside mu
	warnf    func(format string, args ...any) // optional, invoked outside mu

	mu       sync.Mutex
	order    []string
	sigs     map[string]fileSig
	versions map[string]*atomic.Uint64
	missing  map[string]bool // disappearance already announced
	subs     map[*Subscription]struct{}
	stop     chan struct{}
	done     chan struct{}
}

// NewFileWatcher builds a watcher for paths, capturing their initial
// signatures. Versions start at 1 so "version > last-sent" is true for
// every file on a recipient's initial pass.
func NewFileWatcher(paths []string, interval time.Duration) (*FileWatcher, error) {
	if interval <= 0 {
		interval = WatchPollInterval
	}
	w := &FileWatcher{
		interval: interval,
		sigs:     make(map[string]fileSig, len(paths)),
		versions: make(map[string]*atomic.Uint64, len(paths)),
		missing:  make(map[string]bool, len(paths)),
		subs:     make(map[*Subscription]struct{}),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	for _, p := range paths {
		sig, err := statSig(p)
		if err != nil {
			return nil, fmt.Errorf("watching %s: %w", p, err)
		}
		if _, dup := w.sigs[p]; dup {
			return nil, fmt.Errorf("watching %s: duplicate path", p)
		}
		w.order = append(w.order, p)
		w.sigs[p] = sig
		var v atomic.Uint64
		v.Store(1)
		w.versions[p] = &v
	}
	return w, nil
}

// SetChangeCallback installs fn, invoked with the base name of each path
// whose version was just bumped. It is called outside the watcher lock
// (the snapshot of changed paths is taken first), so fn may take other
// locks. Call before Start.
func (w *FileWatcher) SetChangeCallback(fn func(name string)) {
	w.mu.Lock()
	w.onChange = fn
	w.mu.Unlock()
}

// SetWarnCallback installs fn for watcher diagnostics (a watched file
// vanishing or unstatable). It is invoked outside the watcher lock; the
// default drops these lines. Call before Start.
func (w *FileWatcher) SetWarnCallback(fn func(format string, args ...any)) {
	w.mu.Lock()
	w.warnf = fn
	w.mu.Unlock()
}

func (w *FileWatcher) warn(format string, args ...any) {
	w.mu.Lock()
	fn := w.warnf
	w.mu.Unlock()
	if fn != nil {
		fn(format, args...)
	}
}

// Start launches the poll goroutine. Stop it with Close.
func (w *FileWatcher) Start() {
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				w.poll()
			}
		}
	}()
}

// poll re-stats every watched path; on signature change (or reappearance
// after a disappearance, even with an unchanged signature) it bumps the
// version. The state lock is held only for the stat sweep; the change
// callback and subscription signals fire after it is released so external
// callbacks can never deadlock against watcher callers.
func (w *FileWatcher) poll() {
	w.mu.Lock()
	var changed []string
	for _, p := range w.order {
		sig, err := statSig(p)
		if err != nil {
			if os.IsNotExist(err) {
				if !w.missing[p] {
					w.missing[p] = true
					w.warn("catpult: watched file %s is missing; waiting for it to reappear\n", p)
				}
				// Version preserved until the path reappears.
				continue
			}
			w.warn("catpult: stat %s: %v\n", p, err)
			continue
		}
		wasMissing := w.missing[p]
		w.missing[p] = false
		if wasMissing || sig != w.sigs[p] {
			w.sigs[p] = sig
			w.versions[p].Add(1)
			changed = append(changed, p)
		}
	}
	subs := make([]*Subscription, 0, len(w.subs))
	for sub := range w.subs {
		subs = append(subs, sub)
	}
	onChange := w.onChange
	w.mu.Unlock()

	if len(changed) == 0 {
		return
	}
	if onChange != nil {
		for _, p := range changed {
			onChange(filepath.Base(p))
		}
	}
	for _, sub := range subs {
		sub.signal()
	}
}

// Version reports the current monitor version of path (0 = unknown).
// Callers capture it before sending and compare after/during, which is
// what makes in-flight transfers self-abort on change. Lock-free: the
// hot per-chunk check never contends with the poller.
func (w *FileWatcher) Version(path string) uint64 {
	if v, ok := w.versions[path]; ok {
		return v.Load()
	}
	return 0
}

// Subscribe registers a new recipient connection's view of changes.
func (w *FileWatcher) Subscribe() *Subscription {
	s := &Subscription{w: w, notify: make(chan struct{}, 1), done: w.done}
	w.mu.Lock()
	if w.subs != nil {
		w.subs[s] = struct{}{}
	}
	w.mu.Unlock()
	return s
}

// Close stops polling and releases every subscription: their Done()
// channels close as done closes, unblocking all watch handlers.
func (w *FileWatcher) Close() {
	close(w.stop)
	<-w.done
	w.mu.Lock()
	w.subs = nil
	w.mu.Unlock()
}

// Subscription is one recipient connection's view of watcher changes.
// Changes() is coalesced: a slow or blocked recipient is never stalled
// by the poller and never stalls other recipients.
type Subscription struct {
	w      *FileWatcher
	notify chan struct{}
	done   chan struct{}
}

// Changes yields coalesced "something changed" signals.
func (s *Subscription) Changes() <-chan struct{} { return s.notify }

// Done closes when the watcher is closed (room shutdown).
func (s *Subscription) Done() <-chan struct{} { return s.done }

// signal delivers one non-blocking wakeup; a pending signal is left as-is
// so consecutive changes coalesce into a single wakeup.
func (s *Subscription) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Close removes the subscription from the watcher.
func (s *Subscription) Close() {
	s.w.mu.Lock()
	if s.w.subs != nil {
		delete(s.w.subs, s)
	}
	s.w.mu.Unlock()
}

// WatchEvent describes one watch-mode serving event for callers that want
// stderr progress lines.
type WatchEvent struct {
	Kind WatchEventKind
	Name string
	Size int64
}

type WatchEventKind int

const (
	// WatchSending is emitted before each version of a file is sent.
	WatchSending WatchEventKind = iota
	// WatchMissing marks a watched file absent on disk (skipped until it
	// reappears; only reachable on a recipient's initial pass).
	WatchMissing
)

// WatchEventKind makes %v output readable.
func (k WatchEventKind) String() string {
	switch k {
	case WatchSending:
		return "sending"
	case WatchMissing:
		return "missing"
	default:
		return "unknown"
	}
}

// ServeWatchConn serves one recipient for the room's lifetime: ROOM_INFO
// announcing file-watch semantics, an initial pass sending every watched
// file, then re-sends whenever any watched file changes. It closes conn
// when its serving ends — exactly like file mode, whose handler closes
// the connection to signal the transfer is over; in watch mode serving
// ends only at room close (watcher shutdown) or when the recipient goes
// away (write error), so the room-end close matches the file-sender
// close sequence. It returns only after room end / recipient gone.
func ServeWatchConn(conn net.Conn, w *FileWatcher, files []string, onEvent func(WatchEvent)) error {
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	if err := protocol.WriteFrame(conn, protocol.MsgRoomInfo, protocol.EncodeRoomInfo(protocol.PayloadFileWatch)); err != nil {
		return err
	}
	sub := w.Subscribe()
	defer sub.Close()
	closing := func() bool {
		select {
		case <-sub.Done():
			return true
		default:
			return false
		}
	}

	lastSent := make(map[string]uint64, len(files))
	sendAll := func() error {
		for _, path := range files {
			if err := sendWatchedFile(conn, w, path, lastSent, closing, onEvent); err != nil {
				return err
			}
		}
		return nil
	}
	if err := sendAll(); err != nil {
		if errors.Is(err, errWatcherClosed) {
			return nil
		}
		return err
	}
	for {
		select {
		case <-sub.Done():
			return nil
		case <-sub.Changes():
			if err := sendAll(); err != nil {
				if errors.Is(err, errWatcherClosed) {
					return nil
				}
				return err
			}
		}
	}
}

// sendWatchedFile sends path when its current version is newer than the
// last version cleanly sent to this recipient. If the file changes
// mid-send, the transfer aborts at a chunk boundary (no FILE_END) and
// restarts with a fresh FILE_HEADER — the in-flight version is superseded
// (receiver discards the partial on the next header). lastSent is updated
// only after a clean send. A file missing on disk is skipped, not fatal
// (it may reappear via an atomic save). closing reports room shutdown;
// when it turns true the send aborts at the next chunk boundary with
// errWatcherClosed promptly, without severing the connection mid-chunk.
func sendWatchedFile(conn net.Conn, w *FileWatcher, path string, lastSent map[string]uint64, closing func() bool, onEvent func(WatchEvent)) error {
	if w.Version(path) <= lastSent[path] || closing() {
		return nil
	}
	for {
		ver := w.Version(path)
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				if onEvent != nil {
					onEvent(WatchEvent{Kind: WatchMissing, Name: filepath.Base(path)})
				}
				return nil // skip; may reappear
			}
			return fmt.Errorf("opening %s: %w", path, err)
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if !fi.Mode().IsRegular() {
			f.Close()
			return fmt.Errorf("%s is not a regular file", path)
		}
		changed := func() bool { return w.Version(path) != ver || closing() }
		err = writeFile(conn, filepath.Base(path), path, f, fi.Size(), changed, watchSendingEvent(onEvent))
		switch {
		case err == nil:
			// Clean FILE_END just written; if the file changed during the
			// final moments, resend so the recipient ends on the newest.
			if changed() {
				if closing() {
					return nil // room ending mid-version: recipient sees the close
				}
				continue
			}
			lastSent[path] = ver
			return nil
		case errors.Is(err, errFileChanged):
			if closing() {
				return errWatcherClosed
			}
			continue // supersede: fresh header, new version
		default:
			return err
		}
	}
}

// errWatcherClosed reports that the room is shutting down; an in-flight
// watched-file send aborted at a chunk boundary and no version send is
// pending, so ServeWatchConn ends cleanly (closing conn) instead of
// reporting a serving error.
var errWatcherClosed = errors.New("watcher closed")

// watchSendingEvent adapts a WatchEvent sink into the per-transfer onFile
// hook used by the finite sender, so both report the same stderr line.
func watchSendingEvent(onEvent func(WatchEvent)) func(name string, size int64) {
	if onEvent == nil {
		return nil
	}
	return func(name string, size int64) {
		onEvent(WatchEvent{Kind: WatchSending, Name: name, Size: size})
	}
}
