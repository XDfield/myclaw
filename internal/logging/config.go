package logging

// LogConfig holds logging configuration.
type LogConfig struct {
	Level   string `json:"level,omitempty"`   // "debug","info","warn","error"; default "info"
	Dir     string `json:"dir,omitempty"`     // log directory; default ~/.myclaw/logs
	Console bool   `json:"console,omitempty"` // also write to stderr; default true
	MaxDays int    `json:"maxDays,omitempty"` // days to keep old log files; default 30, 0=no cleanup
}

// DefaultLogConfig returns sensible defaults for logging.
func DefaultLogConfig() LogConfig {
	return LogConfig{
		Level:   "info",
		Console: true,
		MaxDays: 30,
	}
}
