package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/validaoxyz/hyperliquid-exporter/internal/config"
	"github.com/validaoxyz/hyperliquid-exporter/internal/exporter"
	"github.com/validaoxyz/hyperliquid-exporter/internal/logger"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: hl_exporter <command> [options]")
		fmt.Println("Commands:")
		fmt.Println("  start    Start the Hyperliquid exporter")
		fmt.Println("\nOptions:")
		fmt.Println("  --config     Path to configuration file (default: \"config.yaml\")")
		fmt.Println("  --log-level  Set the logging level (default: \"debug\")")
		fmt.Println("               Available levels: debug, info, warning, error")
		os.Exit(1)
	}

	startCmd := flag.NewFlagSet("start", flag.ExitOnError)
	logLevel := startCmd.String("log-level", "debug", "Log level (debug, info, warning, error)")

	switch os.Args[1] {
	case "start":
		startCmd.Parse(os.Args[2:])
	default:
		fmt.Printf("%q is not a valid command.\n", os.Args[1])
		os.Exit(1)
	}

	// Set log level
	if err := logger.SetLogLevel(*logLevel); err != nil {
		fmt.Printf("Error setting log level: %v\n", err)
		os.Exit(1)
	}

	// Load configuration
	cfg := config.LoadConfig()

	// Start the exporter
	exporter.Start(cfg)

	// Keep the program running
	select {}
}
