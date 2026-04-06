package ui

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// LogFile represents an open per-task log file. Callers create one with
// OpenLogFile, install its slog.Handler as the global default for the run,
// and Close it when the run finishes.
type LogFile struct {
	path string
	f    *os.File
}

// OpenLogFile creates ~/.vairdict/logs/<taskID>.log (and parents) and
// returns a LogFile handle. The directory is created with 0700 since logs
// may contain prompt content. If $HOME is unwritable, returns an error and
// callers should fall back to the existing slog handler.
func OpenLogFile(taskID string) (*LogFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home dir: %w", err)
	}
	dir := filepath.Join(home, ".vairdict", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating log dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, taskID+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening log file %s: %w", path, err)
	}
	return &LogFile{path: path, f: f}, nil
}

// Path returns the absolute filesystem path of the log file.
func (l *LogFile) Path() string { return l.path }

// Handler returns a JSON slog handler that writes to this log file. Pair
// with slog.SetDefault(slog.New(l.Handler())) at the start of a run.
func (l *LogFile) Handler() slog.Handler {
	return slog.NewJSONHandler(l.f, &slog.HandlerOptions{Level: slog.LevelDebug})
}

// Close flushes and closes the underlying file.
func (l *LogFile) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	return l.f.Close()
}
