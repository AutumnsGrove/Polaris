package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewRotatingWriter_CreatesTodaysFile(t *testing.T) {
	dir := t.TempDir()
	w, err := newRotatingWriter(dir)
	if err != nil {
		t.Fatalf("newRotatingWriter returned error: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	if _, err := os.Stat(filepath.Join(dir, today+".log")); err != nil {
		t.Errorf("expected today's log file to exist: %v", err)
	}
	if w.date != today {
		t.Errorf("w.date = %q, want %q", w.date, today)
	}
}

func TestRotatingWriter_WriteAppendsToFile(t *testing.T) {
	dir := t.TempDir()
	w, err := newRotatingWriter(dir)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}

	if _, err := w.Write([]byte("first line\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if _, err := w.Write([]byte("second line\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	contents, err := os.ReadFile(filepath.Join(dir, today+".log"))
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	if string(contents) != "first line\nsecond line\n" {
		t.Errorf("log contents = %q, want both writes appended in order", contents)
	}
}

func TestPruneOld_RemovesOnlyExpiredLogFiles(t *testing.T) {
	dir := t.TempDir()

	old := time.Now().AddDate(0, 0, -(retentionDays+10)).Format("2006-01-02") + ".log"
	recent := time.Now().AddDate(0, 0, -5).Format("2006-01-02") + ".log"
	notOurs := "readme.txt"

	for _, name := range []string{old, recent, notOurs} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing fixture file %s: %v", name, err)
		}
	}

	pruneOld(dir)

	assertExists := func(name string, want bool) {
		_, err := os.Stat(filepath.Join(dir, name))
		exists := err == nil
		if exists != want {
			t.Errorf("file %s exists = %v, want %v", name, exists, want)
		}
	}
	assertExists(old, false)
	assertExists(recent, true)
	assertExists(notOurs, true) // doesn't match YYYY-MM-DD.log — must be left alone
}

func TestPruneOld_MissingDirDoesNotPanic(t *testing.T) {
	pruneOld(filepath.Join(t.TempDir(), "does-not-exist"))
}

func TestWithPrefix_LogsWithoutInit(t *testing.T) {
	// Before Init() is called, package-level loggers fall back to stderr
	// — this should never panic or block, just write somewhere.
	l := WithPrefix("test")
	l.Info("hello")
	l.Warn("warning")
	l.Error("error")
	l.Debug("debug")
}

func TestInit_RoutesWritesToTheLogFile(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	l := WithPrefix("inittest")
	l.Info("after init")

	today := time.Now().Format("2006-01-02")
	contents, err := os.ReadFile(filepath.Join(dir, today+".log"))
	if err != nil {
		t.Fatalf("reading log file after Init: %v", err)
	}
	if len(contents) == 0 {
		t.Error("expected Init to route subsequent writes into the log file")
	}
}
