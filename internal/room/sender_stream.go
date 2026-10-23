package room

import (
	"net"
	"sync"
	"time"

	"catpult/internal/protocol"
)

// Broadcaster holds the in-memory stream cache and the live recipient set.
// Every recipient receives the same byte stream: buffered history first,
// then the live tail. Bounded: history keeps at most protocol.StreamBufferCap
// bytes; on overflow the oldest bytes are evicted (late joiners then see a
// truncated prefix — still in order, still the same stream).
type Broadcaster struct {
	mu    sync.Mutex
	subs  map[*subscriber]struct{}
	hist  []byte
	total uint64 // total bytes ever appended (for future offset use)
}

type subscriber struct {
	conn net.Conn
	ch   chan []byte
	done chan struct{}
}

// writeTimeout bounds a single chunk write to one recipient; a slow or
// dead recipient is dropped without affecting others.
const writeTimeout = 30 * time.Second

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[*subscriber]struct{})}
}

// Publish appends chunk to history (evicting oldest on overflow) and queues
// it to every live recipient without blocking. Never blocks the producer.
func (b *Broadcaster) Publish(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	cp := append([]byte(nil), chunk...)
	b.mu.Lock()
	b.hist = append(b.hist, cp...)
	if overflow := len(b.hist) - protocol.StreamBufferCap; overflow > 0 {
		b.hist = append([]byte(nil), b.hist[overflow:]...)
	}
	b.total += uint64(len(cp))
	for sub := range b.subs {
		select {
		case sub.ch <- cp:
		default:
			// Slow consumer: drop it; the writer loop will exit and
			// clean up. Closing done here would race with Serve; just
			// remove from the set and close the conn — the writer
			// observes the close and exits.
			delete(b.subs, sub)
			_ = sub.conn.Close()
		}
	}
	b.mu.Unlock()
}

// Serve handles one recipient: ROOM_INFO(stream), buffered history in
// order, then the live tail until the room ends or the peer goes away.
// The subscriber is registered before history is sent so no byte appended
// concurrently can fall in the gap (appends during the history send are
// also queued to the new subscriber and arrive after, in order).
func (b *Broadcaster) Serve(conn net.Conn) error {
	sub := &subscriber{conn: conn, ch: make(chan []byte, 64), done: make(chan struct{})}
	b.mu.Lock()
	hist := append([]byte(nil), b.hist...)
	b.subs[sub] = struct{}{}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.subs, sub)
		b.mu.Unlock()
		_ = conn.Close()
	}()

	if err := conn.SetWriteDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	if err := protocol.WriteFrame(conn, protocol.MsgRoomInfo, protocol.EncodeRoomInfo(protocol.PayloadStream)); err != nil {
		return err
	}
	for off := 0; off < len(hist); {
		end := min(off+protocol.ChunkSize, len(hist))
		if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			return err
		}
		if err := protocol.WriteFrame(conn, protocol.MsgStreamChunk, hist[off:end]); err != nil {
			return err
		}
		off = end
	}
	// Live tail: ordered, one writer per conn.
	for {
		select {
		case chunk, ok := <-sub.ch:
			if !ok {
				return nil
			}
			if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				return err
			}
			if err := protocol.WriteFrame(conn, protocol.MsgStreamChunk, chunk); err != nil {
				return err
			}
		case <-sub.done:
			return nil
		}
	}
}

// Shutdown sends MsgStreamEnd to every live recipient, then closes
// their connections. Best effort: slow or dead recipients are dropped
// without blocking room teardown. CloseAll is the forceful alternative.
func (b *Broadcaster) Shutdown() {
	b.mu.Lock()
	subs := make(map[*subscriber]struct{}, len(b.subs))
	for sub := range b.subs {
		subs[sub] = struct{}{}
	}
	b.mu.Unlock()

	for sub := range subs {
		_ = sub.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		_ = protocol.WriteFrame(sub.conn, protocol.MsgStreamEnd, nil)
		b.mu.Lock()
		if _, ok := b.subs[sub]; ok {
			select {
			case <-sub.done:
			default:
				close(sub.done)
			}
			delete(b.subs, sub)
		}
		b.mu.Unlock()
		_ = sub.conn.Close()
	}
}

// CloseAll disconnects every live recipient (room shutdown).
func (b *Broadcaster) CloseAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs {
		select {
		case <-sub.done:
		default:
			close(sub.done)
		}
		_ = sub.conn.Close()
		delete(b.subs, sub)
	}
}

// Count reports live recipients (informational only).
func (b *Broadcaster) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
