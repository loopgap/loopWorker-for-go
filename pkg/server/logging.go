package server

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"loopworker/internal/config"
	"loopworker/pkg/logger"
)

// SetupLogging configures the process logger from configuration. An unknown
// level or destination is an error here too, so a typo cannot silently downgrade
// the logs an operator needs to diagnose a failure.
func SetupLogging(cfg config.LoggingConfig) error {
	var level zapcore.Level
	switch strings.ToLower(strings.TrimSpace(cfg.Level)) {
	case "debug":
		level = zapcore.DebugLevel
	case "info", "":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	default:
		return fmt.Errorf("logging.level %q is not one of: %s", cfg.Level, config.AcceptedLogLevels)
	}

	var encoder zapcore.Encoder
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "ts"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "json":
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	case "text", "":
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	default:
		return fmt.Errorf("logging.format %q is not one of: %s", cfg.Format, config.AcceptedLogFormats)
	}

	var out *os.File
	switch strings.ToLower(strings.TrimSpace(cfg.Output)) {
	case "stdout", "":
		out = os.Stdout
	case "stderr":
		out = os.Stderr
	default:
		return fmt.Errorf("logging.output %q is not one of: %s", cfg.Output, config.AcceptedLogOutputs)
	}

	logger.Set(zap.New(
		zapcore.NewCore(encoder, zapcore.AddSync(out), level),
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	))
	return nil
}
