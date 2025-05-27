package dcron

import (
	"log"
	"os"
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
		logger: log.New(os.Stderr, "", log.LstdFlags|log.Lshortfile),
	}
}

func (l *stdLogger) Debug(args ...interface{}) {
	if l.level <= LevelDebug {
		l.logger.Println(append([]interface{}{"[DEBUG]"}, args...)...)
	}
}

func (l *stdLogger) Info(args ...interface{}) {
	if l.level <= LevelInfo {
		l.logger.Println(append([]interface{}{"[INFO]"}, args...)...)
	}
}

func (l *stdLogger) Warn(args ...interface{}) {
	if l.level <= LevelWarn {
		l.logger.Println(append([]interface{}{"[WARN]"}, args...)...)
	}
}

func (l *stdLogger) Error(args ...interface{}) {
	if l.level <= LevelError {
		l.logger.Println(append([]interface{}{"[ERROR]"}, args...)...)
	}
}

func (l *stdLogger) Fatal(args ...interface{}) {
	if l.level <= LevelFatal {
		l.logger.Fatal(append([]interface{}{"[FATAL]"}, args...)...)
	}
}

func (l *stdLogger) Debugf(format string, args ...interface{}) {
	if l.level <= LevelDebug {
		l.logger.Printf("[DEBUG] "+format, args...)
	}
}

func (l *stdLogger) Infof(format string, args ...interface{}) {
	if l.level <= LevelInfo {
		l.logger.Printf("[INFO] "+format, args...)
	}
}

func (l *stdLogger) Warnf(format string, args ...interface{}) {
	if l.level <= LevelWarn {
		l.logger.Printf("[WARN] "+format, args...)
	}
}

func (l *stdLogger) Errorf(format string, args ...interface{}) {
	if l.level <= LevelError {
		l.logger.Printf("[ERROR] "+format, args...)
	}
}

func (l *stdLogger) Fatalf(format string, args ...interface{}) {
	if l.level <= LevelFatal {
		l.logger.Fatalf("[FATAL] "+format, args...)
	}
}
