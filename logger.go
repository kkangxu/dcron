package dcron

import (
	"fmt"
	"log"
	"os"
	"runtime"
)

var logger Logger // global logger

type Logger interface {
	Debug(args ...interface{})
	Info(args ...interface{})
	Warn(args ...interface{})
	Error(args ...interface{})
	Fatal(args ...interface{})
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	Fatalf(format string, args ...interface{})
}

type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

var _ Logger = (*stdLogger)(nil) // ensure stdLogger implements Logger

type stdLogger struct {
	level  LogLevel
	logger *log.Logger
}

func DefaultLogger(level LogLevel) Logger {
	return newStdLogger(level)
}

func newStdLogger(level LogLevel) Logger {
	return &stdLogger{
		level:  level,
		logger: log.New(os.Stderr, "", log.LstdFlags),
	}
}

// getCallerInfo returns the file name and line number of the caller's caller
func getCallerInfo(skip int) string {
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "???:0"
	}
	// Extract just the file name, not the full path
	short := file
	for i := len(file) - 1; i > 0; i-- {
		if file[i] == '/' {
			short = file[i+1:]
			break
		}
	}
	return fmt.Sprintf("%s:%d", short, line)
}

func (l *stdLogger) Debug(args ...interface{}) {
	if l.level <= LevelDebug {
		caller := getCallerInfo(2) // skip 2 levels to get the caller of Debug()
		l.logger.Println(append([]interface{}{"[DEBUG]", caller}, args...)...)
	}
}

func (l *stdLogger) Info(args ...interface{}) {
	if l.level <= LevelInfo {
		caller := getCallerInfo(2)
		l.logger.Println(append([]interface{}{"[INFO]", caller}, args...)...)
	}
}

func (l *stdLogger) Warn(args ...interface{}) {
	if l.level <= LevelWarn {
		caller := getCallerInfo(2)
		l.logger.Println(append([]interface{}{"[WARN]", caller}, args...)...)
	}
}

func (l *stdLogger) Error(args ...interface{}) {
	if l.level <= LevelError {
		caller := getCallerInfo(2)
		l.logger.Println(append([]interface{}{"[ERROR]", caller}, args...)...)
	}
}

func (l *stdLogger) Fatal(args ...interface{}) {
	if l.level <= LevelFatal {
		caller := getCallerInfo(2)
		l.logger.Fatal(append([]interface{}{"[FATAL]", caller}, args...)...)
	}
}

func (l *stdLogger) Debugf(format string, args ...interface{}) {
	if l.level <= LevelDebug {
		caller := getCallerInfo(2)
		l.logger.Printf("[DEBUG] %s "+format, append([]interface{}{caller}, args...)...)
	}
}

func (l *stdLogger) Infof(format string, args ...interface{}) {
	if l.level <= LevelInfo {
		caller := getCallerInfo(2)
		l.logger.Printf("[INFO] %s "+format, append([]interface{}{caller}, args...)...)
	}
}

func (l *stdLogger) Warnf(format string, args ...interface{}) {
	if l.level <= LevelWarn {
		caller := getCallerInfo(2)
		l.logger.Printf("[WARN] %s "+format, append([]interface{}{caller}, args...)...)
	}
}

func (l *stdLogger) Errorf(format string, args ...interface{}) {
	if l.level <= LevelError {
		caller := getCallerInfo(2)
		l.logger.Printf("[ERROR] %s "+format, append([]interface{}{caller}, args...)...)
	}
}

func (l *stdLogger) Fatalf(format string, args ...interface{}) {
	if l.level <= LevelFatal {
		caller := getCallerInfo(2)
		l.logger.Fatalf("[FATAL] %s "+format, append([]interface{}{caller}, args...)...)
	}
}
