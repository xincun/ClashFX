package applog

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxBytes   int64 = 10 << 20 // 10 MiB per file
	DefaultMaxBackups int   = 5        // Keep .1 .. .5 rotated archives
	timeLayout              = "2006-01-02T15:04:05.000Z07:00"
)

// RotatingFile is a goroutine-safe, append-only log file with size-based rotation.
// It flushes every write directly to the OS file descriptor so crash logs survive
// abrupt process termination.
type RotatingFile struct {
	mu         sync.Mutex
	path       string
	maxBytes   int64
	maxBackups int
	file       *os.File
	size       int64
	closed     bool
}

func NewRotatingFile(path string, maxBytes int64, maxBackups int) (*RotatingFile, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return nil, fmt.Errorf("log file path is required")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxBackups < 0 {
		maxBackups = DefaultMaxBackups
	}
	if err := os.MkdirAll(filepath.Dir(cleaned), 0755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", filepath.Dir(cleaned), err)
	}
	rf := &RotatingFile{
		path:       cleaned,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
	}
	if err := rf.openLocked(); err != nil {
		return nil, err
	}
	return rf, nil
}

func (r *RotatingFile) openLocked() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", r.path, err)
	}
	var sz int64
	if st, sErr := f.Stat(); sErr == nil {
		sz = st.Size()
	}
	r.file = f
	r.size = sz
	return nil
}

func (r *RotatingFile) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Write implements io.Writer.
func (r *RotatingFile) Write(p []byte) (int, error) {
	if r == nil || len(p) == 0 {
		return len(p), nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if r.file == nil {
		if err := r.openLocked(); err != nil {
			return 0, err
		}
	}
	if r.maxBytes > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			// Best-effort continue writing even if rename failed temporarily on Windows.
			if r.file == nil {
				if openErr := r.openLocked(); openErr != nil {
					return 0, openErr
				}
			}
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) rotateLocked() error {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	if r.maxBackups == 0 {
		_ = os.Remove(r.path)
		return r.openLocked()
	}

	oldest := fmt.Sprintf("%s.%d", r.path, r.maxBackups)
	_ = os.Remove(oldest)
	for i := r.maxBackups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", r.path, i)
		dst := fmt.Sprintf("%s.%d", r.path, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = renameWithRetry(src, dst)
		}
	}
	if err := renameWithRetry(r.path, r.path+".1"); err != nil {
		// Fall back to truncating current file if rename is locked by an external reader on Windows.
		f, oErr := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if oErr != nil {
			return err
		}
		r.file = f
		r.size = 0
		return nil
	}
	return r.openLocked()
}

func renameWithRetry(src, dst string) error {
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 5
	}
	var err error
	for i := 0; i < attempts; i++ {
		_ = os.Remove(dst)
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if i+1 < attempts {
			time.Sleep(40 * time.Millisecond)
		}
	}
	return err
}

// WriteLine writes a single line with newline normalization.
func (r *RotatingFile) WriteLine(line string) {
	if r == nil {
		return
	}
	clean := strings.TrimRight(line, "\r\n")
	var buf bytes.Buffer
	buf.Grow(len(clean) + 1)
	buf.WriteString(clean)
	buf.WriteByte('\n')
	_, _ = r.Write(buf.Bytes())
}

// WriteStampedLine writes a line prefixed with an RFC3339-millisecond timestamp and tag.
func (r *RotatingFile) WriteStampedLine(tag, line string) {
	if r == nil {
		return
	}
	clean := strings.TrimRight(line, "\r\n")
	ts := time.Now().Format(timeLayout)
	if tag != "" {
		r.WriteLine(fmt.Sprintf("%s [%s] %s", ts, tag, clean))
		return
	}
	r.WriteLine(fmt.Sprintf("%s %s", ts, clean))
}

// Logf writes a structured timestamped event line.
func (r *RotatingFile) Logf(component, level, format string, args ...any) {
	if r == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format(timeLayout)
	r.WriteLine(fmt.Sprintf("%s [%s] [%s] %s", ts, component, strings.ToUpper(strings.TrimSpace(level)), msg))
}

func (r *RotatingFile) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		return err
	}
	return nil
}

// TailFile reads up to the last maxLines lines from path (walking rotated archives
// .1 .. .5 as needed when the active file was recently rotated).
func TailFile(path string, maxLines int) []string {
	if strings.TrimSpace(path) == "" || maxLines <= 0 {
		return nil
	}
	const maxReadBytes int64 = 256 << 10 // 256 KiB per file
	lines := readTailLinesSingle(path, maxReadBytes)
	for i := 1; i <= DefaultMaxBackups && len(lines) < maxLines; i++ {
		prev := readTailLinesSingle(fmt.Sprintf("%s.%d", path, i), maxReadBytes)
		if len(prev) == 0 {
			break
		}
		combined := make([]string, 0, len(prev)+len(lines))
		combined = append(combined, prev...)
		combined = append(combined, lines...)
		lines = combined
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

func readTailLinesSingle(path string, maxReadBytes int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return nil
	}
	start := int64(0)
	partial := false
	if st.Size() > maxReadBytes {
		start = st.Size() - maxReadBytes
		partial = true
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxReadBytes))
	if err != nil || len(data) == 0 {
		return nil
	}
	if partial {
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			data = data[idx+1:]
		}
	}
	rawLines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
