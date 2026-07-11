package server

import (
	"context"
	"log/slog"

	klog "github.com/go-kratos/kratos/v2/log"
)

// SlogLogger adapts slog to the Kratos log.Logger interface so the
// whole controller logs through one slog handler.
type SlogLogger struct {
	S *slog.Logger
}

// Log implements kratos log.Logger.
func (l SlogLogger) Log(level klog.Level, keyvals ...any) error {
	var lv slog.Level
	switch level {
	case klog.LevelDebug:
		lv = slog.LevelDebug
	case klog.LevelWarn:
		lv = slog.LevelWarn
	case klog.LevelError, klog.LevelFatal:
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}

	l.S.Log(context.Background(), lv, "kratos", keyvals...)

	return nil
}
