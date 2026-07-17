// Package logger provides a minimal prefixed logger used across all
// packages, matching the WithPrefix/Info/Warn/Debug call shape without
// pulling in an extra dependency.
package logger

import (
	"fmt"
	"log"
	"os"
)

type Logger struct {
	prefix string
	std    *log.Logger
}

func WithPrefix(prefix string) *Logger {
	return &Logger{prefix: prefix, std: log.New(os.Stderr, "", log.LstdFlags)}
}

func (l *Logger) fields(kv []interface{}) string {
	if len(kv) == 0 {
		return ""
	}
	s := ""
	for i := 0; i+1 < len(kv); i += 2 {
		s += fmt.Sprintf(" %v=%v", kv[i], kv[i+1])
	}
	return s
}

func (l *Logger) Info(msg string, kv ...interface{})  { l.std.Printf("[%s] INFO  %s%s", l.prefix, msg, l.fields(kv)) }
func (l *Logger) Warn(msg string, kv ...interface{})  { l.std.Printf("[%s] WARN  %s%s", l.prefix, msg, l.fields(kv)) }
func (l *Logger) Error(msg string, kv ...interface{}) { l.std.Printf("[%s] ERROR %s%s", l.prefix, msg, l.fields(kv)) }
func (l *Logger) Debug(msg string, kv ...interface{}) { l.std.Printf("[%s] DEBUG %s%s", l.prefix, msg, l.fields(kv)) }

func (l *Logger) Infof(format string, args ...interface{}) {
	l.std.Printf("[%s] INFO  "+format, append([]interface{}{l.prefix}, args...)...)
}

func (l *Logger) Warnf(format string, args ...interface{}) {
	l.std.Printf("[%s] WARN  "+format, append([]interface{}{l.prefix}, args...)...)
}
