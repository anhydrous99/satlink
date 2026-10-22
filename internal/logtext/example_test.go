package logtext_test

import (
	"fmt"
	"log/slog"

	"github.com/anhydrous99/satlink/internal/logtext"
)

// A handler that prints records as the command-line tool does.
func ExampleNewHandler() {
	handler := logtext.NewHandler(slog.LevelInfo, func(level slog.Level, pkg, text string) {
		fmt.Printf("[%-5s %s] %s\n", logtext.LevelName(level), pkg, text)
	})
	logger := slog.New(handler)

	logger.Info("IPv4 up", "pkg", "satnet", "address", "10.64.0.2")
	logger.With("pkg", "sim").Warn("simulated gateway: no route", "to", "192.0.2.99")
	logger.Debug("not shown: below the minimum")
	// Output:
	// [INFO  satnet] IPv4 up address=10.64.0.2
	// [WARN  sim] simulated gateway: no route to=192.0.2.99
}
