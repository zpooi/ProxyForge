// Package applog stores the standard application log for the current local
// calendar day. Older ProxyForge log files are removed during startup and at
// midnight rotation.
package applog

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const filePrefix = "proxyforge-"

type Chunk struct {
	Date    string
	Content string
	Next    int64
	More    bool
}

type Store struct {
	mu   sync.Mutex
	dir  string
	date string
	path string
	file *os.File
	now  func() time.Time
}

func New(dir string) (*Store, error) {
	return newStore(dir, time.Now)
}

func newStore(dir string, now func() time.Time) (*Store, error) {
	if dir == "" {
		dir = "logs"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, now: now}
	if err := s.rotateLocked(); err != nil {
		if s.file != nil {
			_ = s.file.Close()
		}
		return nil, err
	}
	return s, nil
}

func (s *Store) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureCurrentLocked(); err != nil {
		return 0, err
	}
	return s.file.Write(p)
}

// Read returns at most limit bytes beginning at offset. Callers can continue
// from Next until More is false, then poll Next for newly appended lines.
func (s *Store) Read(offset int64, limit int) (Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureCurrentLocked(); err != nil {
		return Chunk{}, err
	}
	info, err := s.file.Stat()
	if err != nil {
		return Chunk{}, err
	}
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	if limit <= 0 {
		limit = 256 << 10
	}
	remaining := info.Size() - offset
	if remaining > int64(limit) {
		remaining = int64(limit)
	}
	buf := make([]byte, int(remaining))
	if len(buf) > 0 {
		n, readErr := s.file.ReadAt(buf, offset)
		buf = buf[:n]
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return Chunk{}, readErr
		}
	}
	next := offset + int64(len(buf))
	return Chunk{
		Date:    s.date,
		Content: string(buf),
		Next:    next,
		More:    next < info.Size(),
	}, nil
}

// Snapshot returns a consistent copy of today's complete log for download.
func (s *Store) Snapshot() (date string, content []byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureCurrentLocked(); err != nil {
		return "", nil, err
	}
	content, err = os.ReadFile(s.path)
	return s.date, content, err
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

func (s *Store) ensureCurrentLocked() error {
	today := s.now().Format("2006-01-02")
	if s.file != nil && s.date == today {
		return nil
	}
	return s.rotateLocked()
}

func (s *Store) rotateLocked() error {
	today := s.now().Format("2006-01-02")
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return err
		}
		s.file = nil
	}
	path := filepath.Join(s.dir, filePrefix+today+".log")
	if err := s.removeOldLocked(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	s.date = today
	s.path = path
	s.file = file
	return nil
}

func (s *Store) removeOldLocked(currentPath string) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		path := filepath.Join(s.dir, name)
		if path != currentPath {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
