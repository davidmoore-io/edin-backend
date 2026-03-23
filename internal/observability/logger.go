package observability

import "log"

// Logger exposes a minimal structured logging facade.
type Logger struct {
	component string
}

// NewLogger constructs a logger tagged with a component name.
func NewLogger(component string) *Logger {
	return &Logger{component: component}
}

// Info logs an informational message.
func (l *Logger) Info(msg string) {
	log.Printf("[INFO] [%s] %s", l.component, msg)
}

// Warn logs a warning message.
func (l *Logger) Warn(msg string) {
	log.Printf("[WARN] [%s] %s", l.component, msg)
}

// Error logs an error message.
func (l *Logger) Error(msg string, err error) {
	log.Printf("[ERROR] [%s] %s: %v", l.component, msg, err)
}
