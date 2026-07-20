package utils

import (
	"context"
	"fmt"
	"runtime/debug"
)

// Logger is an interface that allows passing standard or custom loggers
type Logger interface {
	Printf(format string, v ...interface{})
}

// DefaultLogger is a fallback logger that prints to standard out
type DefaultLogger struct{}

func (l *DefaultLogger) Printf(format string, v ...interface{}) {
	fmt.Printf("[SafeGo] "+format+"\n", v...)
}

var defaultLogger Logger = &DefaultLogger{}

// SetLogger allows injecting a global logger for all SafeGo panics
func SetLogger(l Logger) {
	if l != nil {
		defaultLogger = l
	}
}

// GoSafe runs the provided function in a new goroutine and recovers from panics.
// This is the fundamental anti-crash primitive for the entire project.
func GoSafe(ctx context.Context, fn func(context.Context)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("panic recovered: %v\nstack trace: %s", r, string(debug.Stack()))
				defaultLogger.Printf("%v", err)
				// In a full integration, we would also emit an event to the EventBus or SelfHealer here
			}
		}()
		fn(ctx)
	}()
}
