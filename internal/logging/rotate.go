package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DailyRotateWriter is an io.Writer that automatically rotates log files
// by date. File naming: <prefix>-YYYY-MM-DD.log
// On each Write call it checks whether the date has changed; if so it
// closes the current file and opens a new one. Old files beyond maxDays
// are removed on startup and at each rotation.
type DailyRotateWriter struct {
	dir     string
	prefix  string
	maxDays int

	mu       sync.Mutex
	current  *os.File
	currDate string // "2006-01-02"
}

// NewDailyRotateWriter creates a new date-based rotating writer.
// dir is the directory for log files, prefix is the filename prefix,
// and maxDays controls how many days of logs to keep (0 = keep all).
func NewDailyRotateWriter(dir, prefix string, maxDays int) *DailyRotateWriter {
	w := &DailyRotateWriter{
		dir:     dir,
		prefix:  prefix,
		maxDays: maxDays,
	}
	return w
}

// Write implements io.Writer. It ensures the correct daily file is open
// and writes p to it.
func (w *DailyRotateWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if today != w.currDate || w.current == nil {
		if rotateErr := w.rotateLocked(today); rotateErr != nil {
			return 0, rotateErr
		}
	}
	return w.current.Write(p)
}

// Close closes the underlying file.
func (w *DailyRotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current != nil {
		err := w.current.Close()
		w.current = nil
		return err
	}
	return nil
}

// rotateLocked switches to a new log file for the given date string.
// Caller must hold w.mu.
func (w *DailyRotateWriter) rotateLocked(date string) error {
	if w.current != nil {
		_ = w.current.Close()
		w.current = nil
	}

	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("create log dir %s: %w", w.dir, err)
	}

	filename := fmt.Sprintf("%s-%s.log", w.prefix, date)
	path := filepath.Join(w.dir, filename)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", path, err)
	}

	w.current = f
	w.currDate = date

	// Clean up old files in background (best-effort).
	if w.maxDays > 0 {
		go w.cleanup()
	}

	return nil
}

// cleanup removes log files older than maxDays.
func (w *DailyRotateWriter) cleanup() {
	if w.maxDays <= 0 {
		return
	}

	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -w.maxDays).Format("2006-01-02")
	prefix := w.prefix + "-"
	suffix := ".log"

	// Collect dated filenames to sort and evaluate.
	type dated struct {
		name string
		date string
	}
	var candidates []dated
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		dateStr := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		if len(dateStr) != 10 { // "2006-01-02"
			continue
		}
		candidates = append(candidates, dated{name: name, date: dateStr})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].date < candidates[j].date
	})

	for _, c := range candidates {
		if c.date < cutoff {
			_ = os.Remove(filepath.Join(w.dir, c.name))
		}
	}
}
