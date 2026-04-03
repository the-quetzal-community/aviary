//go:build !musl

package main

import (
	"log/slog"

	"github.com/quaadgras/velopack-go/velopack"
)

func init() {
	velopack.Run(velopack.App{
		AutoApplyOnStartup: true,
		Logger: func(level, message string) {
			switch level {
			case "error":
				slog.Error(message)
			case "trace":
				slog.Debug(message)
			case "info":
				slog.Info(message)
			}
		},
	})
}
