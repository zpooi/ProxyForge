package proxy

import (
	"io"
	"net"
	"sync"
	"time"
)

const (
	// WARP's userspace network stack can surface a sustained TCP download as
	// many small reads. websocket.NetConn maps every Write to a separate
	// WebSocket message, so forwarding those reads verbatim makes nginx emit a
	// large number of small frames/TLS writes on the high-latency public link.
	// Batch enough data to amortize that overhead, while the short deadline
	// keeps interactive responses from waiting for a full buffer.
	webSocketRelayBatchSize  = 64 << 10
	webSocketRelayBatchDelay = time.Millisecond
)

type webSocketRelayConn struct {
	net.Conn
}

// MarkWebSocketRelayConn marks a stream whose writes become individual
// WebSocket messages. The relay uses the marker to batch only the public WSS
// downlink; legacy HTTP/SOCKS5 connections retain their original behaviour.
func MarkWebSocketRelayConn(conn net.Conn) net.Conn {
	if conn == nil {
		return nil
	}
	return &webSocketRelayConn{Conn: conn}
}

func isWebSocketRelayConn(conn net.Conn) bool {
	_, ok := conn.(*webSocketRelayConn)
	return ok
}

func relayDownstreamWriter(conn net.Conn) (io.Writer, func() error) {
	if !isWebSocketRelayConn(conn) {
		return conn, func() error { return nil }
	}
	w := newTimedBatchWriter(conn, webSocketRelayBatchSize, webSocketRelayBatchDelay)
	return w, w.Flush
}

// timedBatchWriter combines immediately adjacent small writes. A timer makes
// the final partial batch visible even when the source connection remains open
// but temporarily stops producing data.
type timedBatchWriter struct {
	mu         sync.Mutex
	dst        io.Writer
	buf        []byte
	maxSize    int
	maxDelay   time.Duration
	timer      *time.Timer
	generation uint64
	err        error
}

func newTimedBatchWriter(dst io.Writer, maxSize int, maxDelay time.Duration) *timedBatchWriter {
	if maxSize < 1 {
		maxSize = 1
	}
	if maxDelay <= 0 {
		maxDelay = time.Millisecond
	}
	return &timedBatchWriter{
		dst:      dst,
		buf:      make([]byte, 0, maxSize),
		maxSize:  maxSize,
		maxDelay: maxDelay,
	}
}

func (w *timedBatchWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.err != nil {
		return 0, w.err
	}

	written := 0
	for len(p) > 0 {
		space := w.maxSize - len(w.buf)
		if space > len(p) {
			space = len(p)
		}
		wasEmpty := len(w.buf) == 0
		w.buf = append(w.buf, p[:space]...)
		p = p[space:]
		written += space
		if wasEmpty {
			w.scheduleFlushLocked()
		}
		if len(w.buf) == w.maxSize {
			w.cancelFlushLocked()
			if err := w.flushLocked(); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

func (w *timedBatchWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cancelFlushLocked()
	if w.err != nil {
		return w.err
	}
	return w.flushLocked()
}

func (w *timedBatchWriter) scheduleFlushLocked() {
	if w.timer != nil || len(w.buf) == 0 || w.err != nil {
		return
	}
	w.generation++
	generation := w.generation
	w.timer = time.AfterFunc(w.maxDelay, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if generation != w.generation {
			return
		}
		w.timer = nil
		_ = w.flushLocked()
	})
}

func (w *timedBatchWriter) cancelFlushLocked() {
	w.generation++
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (w *timedBatchWriter) flushLocked() error {
	if w.err != nil || len(w.buf) == 0 {
		return w.err
	}
	buf := w.buf
	w.buf = w.buf[:0]
	for len(buf) > 0 {
		n, err := w.dst.Write(buf)
		if err != nil {
			w.err = err
			return err
		}
		if n <= 0 {
			w.err = io.ErrShortWrite
			return w.err
		}
		buf = buf[n:]
	}
	return nil
}
