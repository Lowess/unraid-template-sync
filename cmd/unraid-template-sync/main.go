package main

import (
	"log/slog"
	"os"

	"github.com/Lowess/unraid-template-sync/internal/app"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("version", version)
	if err := app.Run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
