package logging

import (
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"
)

// Logger is the package-level global logger used throughout myclaw.
// After Init is called, all packages should use logging.Logger or
// create sub-loggers from it.
var Logger zerolog.Logger

// Component returns a sub-logger with the "component" field pre-set.
// Each package should call this once at init time:
//
//	var glog = logging.Component("gateway")
func Component(name string) zerolog.Logger {
	return Logger.With().Str("component", name).Logger()
}

// Init initializes the global Logger with the given config.
// It sets up file output with daily rotation and optional console output,
// and bridges the Go standard library "log" package to zerolog so that
// any remaining log.Printf calls are captured.
func Init(cfg LogConfig) {
	// Resolve log directory.
	dir := cfg.Dir
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".myclaw", "logs")
	}

	// Set up zerolog global settings.
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	zerolog.CallerMarshalFunc = shortCallerMarshal

	// File writer with daily rotation.
	fileWriter := NewDailyRotateWriter(dir, "myclaw", cfg.MaxDays)

	// Assemble writers.
	var writers []io.Writer
	writers = append(writers, fileWriter)
	if cfg.Console {
		writers = append(writers, zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: "15:04:05",
		})
	}
	multi := zerolog.MultiLevelWriter(writers...)

	// Parse level.
	level, err := zerolog.ParseLevel(cfg.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}

	// Build the logger.
	Logger = zerolog.New(multi).
		Level(level).
		With().
		Timestamp().
		Caller().
		Logger()

	// Bridge standard library log → zerolog so legacy log.Printf calls
	// are automatically routed to our logger.
	stdWriter := Logger.With().Str("source", "stdlog").Logger()
	log.SetOutput(stdWriter)
	log.SetFlags(0)
}

// shortCallerMarshal strips the module prefix to produce short caller info
// like "internal/gateway/gateway.go:123".
func shortCallerMarshal(pc uintptr, file string, line int) string {
	const marker = "myclaw/"
	if idx := lastIndex(file, marker); idx >= 0 {
		file = file[idx+len(marker):]
	}
	return file + ":" + itoa(line)
}

func lastIndex(s, substr string) int {
	for i := len(s) - len(substr); i >= 0; i-- {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
