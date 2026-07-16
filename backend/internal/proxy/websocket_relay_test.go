package proxy

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

type recordingBatchWriter struct {
	mu     sync.Mutex
	writes [][]byte
	notify chan struct{}
}

func (w *recordingBatchWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes = append(w.writes, bytes.Clone(p))
	w.mu.Unlock()
	if w.notify != nil {
		select {
		case w.notify <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func (w *recordingBatchWriter) snapshot() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.writes...)
}

func TestTimedBatchWriterCoalescesSmallWrites(t *testing.T) {
	dst := &recordingBatchWriter{}
	w := newTimedBatchWriter(dst, 8<<10, time.Hour)
	piece := bytes.Repeat([]byte{0x5a}, 1<<10)
	for i := 0; i < 8; i++ {
		if n, err := w.Write(piece); err != nil || n != len(piece) {
			t.Fatalf("Write() = %d, %v", n, err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	writes := dst.snapshot()
	if len(writes) != 1 || len(writes[0]) != 8<<10 {
		t.Fatalf("underlying writes = %d (%v bytes), want one 8192-byte batch", len(writes), writeLengths(writes))
	}
}

func TestTimedBatchWriterFlushesPartialBatchOnDeadline(t *testing.T) {
	dst := &recordingBatchWriter{notify: make(chan struct{}, 1)}
	w := newTimedBatchWriter(dst, 64<<10, 5*time.Millisecond)
	if _, err := w.Write([]byte("interactive response")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dst.notify:
	case <-time.After(time.Second):
		t.Fatal("partial WebSocket batch was not flushed")
	}
	writes := dst.snapshot()
	if len(writes) != 1 || string(writes[0]) != "interactive response" {
		t.Fatalf("deadline flush = %#v", writes)
	}
}

func TestRelayDownstreamWriterOnlyBatchesMarkedWebSocket(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	raw, flushRaw := relayDownstreamWriter(client)
	if raw != client {
		t.Fatal("ordinary proxy connection was wrapped in WebSocket batching")
	}
	if err := flushRaw(); err != nil {
		t.Fatal(err)
	}

	marked := MarkWebSocketRelayConn(client)
	batched, _ := relayDownstreamWriter(marked)
	if _, ok := batched.(*timedBatchWriter); !ok {
		t.Fatalf("marked WebSocket writer type = %T", batched)
	}
}

func writeLengths(writes [][]byte) []int {
	out := make([]int, len(writes))
	for i := range writes {
		out[i] = len(writes[i])
	}
	return out
}
