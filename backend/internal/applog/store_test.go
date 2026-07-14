package applog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreReadsChunksAndKeepsOnlyCurrentDay(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 14, 23, 59, 0, 0, time.Local)
	store, err := newStore(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.Write([]byte("first line\nsecond line\n")); err != nil {
		t.Fatal(err)
	}
	first, err := store.Read(0, 11)
	if err != nil {
		t.Fatal(err)
	}
	if first.Date != "2026-07-14" || first.Content != "first line\n" || first.Next != 11 || !first.More {
		t.Fatalf("unexpected first chunk: %#v", first)
	}
	second, err := store.Read(first.Next, 64)
	if err != nil {
		t.Fatal(err)
	}
	if second.Content != "second line\n" || second.More {
		t.Fatalf("unexpected second chunk: %#v", second)
	}

	now = now.Add(2 * time.Minute)
	if _, err := store.Write([]byte("new day\n")); err != nil {
		t.Fatal(err)
	}
	date, content, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if date != "2026-07-15" || string(content) != "new day\n" {
		t.Fatalf("snapshot = %q %q", date, content)
	}
	if _, err := os.Stat(filepath.Join(dir, "proxyforge-2026-07-14.log")); !os.IsNotExist(err) {
		t.Fatalf("previous-day log still exists: %v", err)
	}
}

func TestStoreRemovesOldLogsOnStartup(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "proxyforge-2026-07-13.log")
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newStore(dir, func() time.Time {
		return time.Date(2026, 7, 14, 12, 0, 0, 0, time.Local)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old ProxyForge log still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
}
