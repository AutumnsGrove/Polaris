// Package logger wraps charmbracelet/log with daily-rotated file output:
// one file per day under a configured directory (logs/YYYY-MM-DD.log),
// pruning anything older than 90 days. Falls back to stderr-only until
// Init is called.
package logger

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	charm "github.com/charmbracelet/log"
)

const retentionDays = 90

// target is swapped by Init. Package-level loggers created via WithPrefix
// at import time (before main/cmd.Init runs) are constructed with
// dynamicWriter, so they still pick up rotation once Init runs — no need
// to track and retroactively reconfigure every Logger instance.
var (
	mu     sync.Mutex
	target io.Writer = os.Stderr
)

type dynamicWriter struct{}

func (dynamicWriter) Write(p []byte) (int, error) {
	mu.Lock()
	w := target
	mu.Unlock()
	return w.Write(p)
}

// Init enables daily-rotated file logging in dir, in addition to stderr.
// Call once at startup (the `run` command does this; short-lived CLI
// commands like `search`/`update` skip it and just log to stderr).
func Init(dir string) error {
	rw, err := newRotatingWriter(dir)
	if err != nil {
		return err
	}
	mu.Lock()
	target = io.MultiWriter(os.Stderr, rw)
	mu.Unlock()
	return nil
}

type Logger struct {
	l *charm.Logger
}

func WithPrefix(prefix string) *Logger {
	l := charm.NewWithOptions(dynamicWriter{}, charm.Options{
		Prefix:          prefix,
		ReportTimestamp: true,
		TimeFormat:      time.Kitchen,
	})
	return &Logger{l: l}
}

func (lg *Logger) Info(msg string, kv ...interface{})  { lg.l.Info(msg, kv...) }
func (lg *Logger) Warn(msg string, kv ...interface{})  { lg.l.Warn(msg, kv...) }
func (lg *Logger) Error(msg string, kv ...interface{}) { lg.l.Error(msg, kv...) }
func (lg *Logger) Debug(msg string, kv ...interface{}) { lg.l.Debug(msg, kv...) }

func (lg *Logger) Infof(format string, args ...interface{})  { lg.l.Infof(format, args...) }
func (lg *Logger) Warnf(format string, args ...interface{})  { lg.l.Warnf(format, args...) }
func (lg *Logger) Errorf(format string, args ...interface{}) { lg.l.Errorf(format, args...) }

// rotatingWriter opens logs/YYYY-MM-DD.log, swapping to a new file the
// instant a write crosses midnight, and prunes anything past retentionDays
// each time it rotates.
type rotatingWriter struct {
	mu   sync.Mutex
	dir  string
	date string
	file *os.File
}

func newRotatingWriter(dir string) (*rotatingWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	w := &rotatingWriter{dir: dir}
	if err := w.rotate(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) rotate() error {
	today := time.Now().Format("2006-01-02")
	if today == w.date && w.file != nil {
		return nil
	}
	if w.file != nil {
		w.file.Close()
	}
	f, err := os.OpenFile(filepath.Join(w.dir, today+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.file = f
	w.date = today
	pruneOld(w.dir)
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.rotate(); err != nil {
		return 0, err
	}
	return w.file.Write(p)
}

func pruneOld(dir string) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		datePart := strings.TrimSuffix(e.Name(), ".log")
		t, err := time.Parse("2006-01-02", datePart)
		if err != nil {
			continue // not one of ours (YYYY-MM-DD.log) — leave it alone
		}
		if t.Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
