package debuglog

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type Level int

const (
	LevelOff Level = iota
	LevelError
	LevelInfo
	LevelTrace
)

var (
	mu     sync.RWMutex
	level  = LevelOff
	logger = log.New(io.Discard, "", log.LstdFlags|log.Lmicroseconds)
)

func ConfigureFromEnv(path string) (io.Closer, error) {
	nextLevel := parseLevel(os.Getenv("VXMON_DEBUG"))
	if nextLevel == LevelOff {
		SetLevel(LevelOff)
		return nil, nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, err
	}

	mu.Lock()
	level = nextLevel
	logger = log.New(f, "", log.LstdFlags|log.Lmicroseconds)
	mu.Unlock()

	Infof("debug logging enabled level=%s", nextLevel.String())
	return f, nil
}

func SetLevel(next Level) {
	mu.Lock()
	level = next
	if next == LevelOff {
		logger = log.New(io.Discard, "", log.LstdFlags|log.Lmicroseconds)
	}
	mu.Unlock()
}

func Errorf(format string, args ...any) {
	logf(LevelError, "ERROR", format, args...)
}

func Infof(format string, args ...any) {
	logf(LevelInfo, "INFO", format, args...)
}

func Tracef(format string, args ...any) {
	logf(LevelTrace, "TRACE", format, args...)
}

func logf(target Level, prefix string, format string, args ...any) {
	mu.RLock()
	defer mu.RUnlock()
	if level < target {
		return
	}
	logger.Printf(prefix+" "+format, args...)
}
func IsTraceEnabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return level >= LevelTrace
}
func parseLevel(raw string) Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "off", "false", "no":
		return LevelOff
	case "1", "error":
		return LevelError
	case "2", "info":
		return LevelInfo
	case "3", "trace", "debug":
		return LevelTrace
	default:
		return LevelInfo
	}
}

func (l Level) String() string {
	switch l {
	case LevelError:
		return "error"
	case LevelInfo:
		return "info"
	case LevelTrace:
		return "trace"
	default:
		return "off"
	}
}
