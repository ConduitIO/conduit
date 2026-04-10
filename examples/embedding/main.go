package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
)

func main() {
	// Create a context that will be canceled on interrupt signal (Ctrl+C)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case <-signalChan: // First interrupt signal
			fmt.Println("\nInterrupt received, initiating graceful shutdown...")
			cancel()
		case <-ctx.Done():
			// Context canceled by other means
		}
		<-signalChan // Second interrupt signal, force exit
		fmt.Println("Second interrupt received, forcing exit.")
		os.Exit(2)
	}()

	// Get current working directory to resolve paths for config and pipelines
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current working directory: %v\n", err)
		os.Exit(1)
	}

	// Configure Conduit runtime
	cfg := conduit.DefaultConfigWithBasePath(cwd) // Use default config as a base
	cfg.DB.Type = conduit.DBTypeInMemory          // Use in-memory database for simplicity
	cfg.API.Enabled = false                       // Disable HTTP and gRPC APIs
	cfg.Log.Level = "debug"                       // Set logging level to debug for detailed output
	cfg.Log.Format = "cli"                        // Use CLI format for human-readable logs
	// This function must be provided to the config for custom loggers.
	// We're essentially replicating a stripped-down version of pkg/conduit/runtime.newLogger here.
	cfg.Log.NewLogger = func(level string, format string) log.CtxLogger {
		l, _ := zerolog.ParseLevel(level)
		f, _ := log.ParseFormat(format)
		return log.InitLogger(l, f)
	}
	cfg.Pipelines.Path = filepath.Join(cwd, "pipeline.yaml") // Point to our example pipeline config

	// Initialize Conduit runtime
	fmt.Println("Initializing Conduit runtime...")
	runtime, err := conduit.NewRuntime(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize Conduit runtime: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		// Close the database gracefully when the main function exits
		if err := runtime.DB.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to close Conduit database: %v\n", err)
		}
	}()

	// Provision pipelines from the configured path (pipeline.yaml)
	fmt.Println("Provisioning pipelines from pipeline.yaml...")
	err = runtime.ProvisionService.Init(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to provision pipelines: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Pipelines provisioned successfully.")

	// Start the Conduit runtime
	fmt.Println("Starting Conduit main loop (will block until Ctrl+C)...")
	err = runtime.Run(ctx)
	if err != nil && !cerrors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "Conduit runtime error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Conduit runtime stopped.")

	// Optionally, verify pipeline state after shutdown
	pipelines := runtime.Orchestrator.Pipelines.List(ctx)
	if len(pipelines) > 0 {
		fmt.Println("Pipelines still present after shutdown (expected for in-memory DB if not explicitly deleted):")
		for id, p := range pipelines {
			fmt.Printf("  - %s (Status: %s)\n", id, p.GetStatus())
		}
	}
	fmt.Println("Application exiting.")
}
