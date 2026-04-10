# Embedding Conduit in Your Go Application

Conduit is designed to be easily embedded within any Go application, allowing you to leverage its powerful data integration capabilities directly within your own services. This guide will walk you through the process of initializing the Conduit runtime, configuring its services, managing pipelines programmatically or via configuration files, and gracefully starting/stopping it.

## Getting Started

To embed Conduit, you primarily interact with the `conduit.NewRuntime` function, which sets up all necessary internal services and returns an object exposing the core functionalities like the `Orchestrator` and `ProvisioningService`.

### Prerequisites

*   Go 1.20+
*   Familiarity with Go modules.

First, create a new Go module and install the Conduit library:

```bash
mkdir my-conduit-app
cd my-conduit-app
go mod init my-conduit-app
go get github.com/conduitio/conduit
```

## Initializing the Conduit Runtime

The `conduit.NewRuntime` function is your entry point. It takes a `conduit.Config` struct, which defines how Conduit will operate.

Here's a minimal example of how to initialize the runtime:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog" // Needed for zerolog.ParseLevel
)

// newLogger is a helper function to create a Conduit-compatible logger.
// It's similar to the internal newLogger function in pkg/conduit/runtime.
func newLogger(level string, format string) log.CtxLogger {
	l, _ := zerolog.ParseLevel(level)
	f, _ := log.ParseFormat(format)
	return log.InitLogger(l, f)
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		select {
		case <-signalChan:
			fmt.Println("\nInterrupt received, initiating graceful shutdown...")
			cancel()
		case <-ctx.Done():
		}
		<-signalChan
		fmt.Println("Second interrupt received, forcing exit.")
		os.Exit(2)
	}()

	// Use default config as a base, then customize
	cfg := conduit.DefaultConfig()

	// --- Configure Conduit Runtime ---
	// 1. Database: Choose your persistence layer
	// For embedding, an in-memory DB is often convenient for testing,
	// or SQLite for lightweight persistence.
	cfg.DB.Type = conduit.DBTypeInMemory
	// cfg.DB.Type = conduit.DBTypeSQLite
	// cfg.DB.SQLite.Path = "conduit.db" // Path for SQLite DB file
	// cfg.DB.Postgres.ConnectionString = "postgres://user:password@host:port/database"
	// You can also provide your own `database.DB` implementation
	// cfg.DB.Driver = myCustomDBDriver

	// 2. API: Disable Conduit's built-in HTTP/gRPC APIs if you're embedding
	// and providing your own API or simply using it for data processing.
	cfg.API.Enabled = false

	// 3. Logging: Configure how Conduit logs its operations
	cfg.Log.Level = "info" // "debug", "info", "warn", "error"
	cfg.Log.Format = "cli" // "cli" or "json"
	cfg.Log.NewLogger = newLogger // Provide the logger constructor

	// 4. Plugins: Register custom built-in connectors/processors if needed
	// cfg.ConnectorPlugins["my-custom-source"] = &myCustomSource{}
	// cfg.ProcessorPlugins["my-custom-processor"] = myCustomProcessorConstructor

	// --- Initialize Conduit ---
	runtime, err := conduit.NewRuntime(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize Conduit runtime: %v\n", err)
		os.Exit(1)
	}
	// Ensure the database is closed on exit
	defer func() {
		if err := runtime.DB.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to close Conduit database: %v\n", err)
		}
	}()

	fmt.Println("Conduit runtime initialized.")

	// ... now you can use runtime.Orchestrator or runtime.ProvisionService
	// and start the runtime.Run(ctx)
}
```

### Understanding `conduit.Config`

The `conduit.Config` struct (`pkg/conduit/config.go`) offers extensive customization:

*   **`DB`**: Configures the database used by Conduit to store pipeline configurations and state.
    *   **`Type`**: Supported values are `badger`, `postgres`, `inmemory`, `sqlite`.
    *   **`Driver`**: (Advanced) You can provide a custom implementation of the `github.com/conduitio/conduit-commons/database.DB` interface. If `Driver` is set, `Type` and other DB-specific fields are ignored.
    *   **`Badger.Path`**, **`Postgres.ConnectionString`**, etc.: Specific configurations for each database type.
*   **`API.Enabled`**: Set to `false` to disable Conduit's built-in HTTP and gRPC APIs, which is typical for embedded usage where your application provides the interface.
*   **`Log`**: Controls logging behavior.
    *   **`Level`**: `debug`, `info`, `warn`, `error`, `trace`.
    *   **`Format`**: `cli` (human-readable) or `json`.
    *   **`NewLogger`**: A function `func(level, format string) log.CtxLogger` that constructs the logger. This is crucial if you want to integrate Conduit's logging with your application's logging framework.
*   **`Pipelines.Path`**: Specifies a directory or a file where Conduit should look for pipeline configuration files (`.yaml` or `.yml`).
*   **`ConnectorPlugins`**, **`ProcessorPlugins`**: Maps of plugin names to their Go SDK implementations. This is where you would register any custom built-in (not standalone) connectors or processors your application provides. `conduit.DefaultConfig()` already includes Conduit's standard built-in plugins.

## Managing Pipelines

Once the Conduit runtime is initialized, you can interact with its `Orchestrator` and `ProvisioningService` to manage pipelines.

*   `runtime.Orchestrator`: Provides programmatic control to create, update, delete, and start/stop pipelines, connectors, and processors individually.
*   `runtime.ProvisionService`: Used to load and apply pipeline configurations from YAML files.

### Option 1: Provisioning from a Configuration File

This is often the simplest way to manage pipelines, especially if their structure is static or managed externally.

1.  **Create a pipeline YAML file:**
    For this example, let's create `pipeline.yaml` in your project root:
    ```yaml
    # examples/embedding/pipeline.yaml
    - id: my-embedded-pipeline
      status: running
      processors: []
      description: "A simple embedded pipeline"
      source:
        plugin: builtin:generator
        settings:
          recordsPerSecond: "1"
          value: "hello world"
          count: "5" # Stop after 5 records for demonstration
      destinations:
        - plugin: builtin:log
          settings: {}
    ```

2.  **Configure `Pipelines.Path` and call `ProvisionService.Init`:**
    In your `main.go`, ensure `cfg.Pipelines.Path` points to your `pipeline.yaml` (or a directory containing it), then call `runtime.ProvisionService.Init(ctx)`:

    ```go
    // In main() after runtime initialization:

    // Set Pipelines.Path to your configuration file
    cwd, _ := os.Getwd()
    cfg.Pipelines.Path = filepath.Join(cwd, "pipeline.yaml") // Assuming pipeline.yaml is in the current directory

    // Initialize Conduit runtime (as shown in previous section)
    runtime, err := conduit.NewRuntime(cfg)
    // ... error handling and defer db close

    fmt.Println("Provisioning pipelines from pipeline.yaml...")
    err = runtime.ProvisionService.Init(ctx)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Failed to provision pipelines: %v\n", err)
        os.Exit(1)
    }
    fmt.Println("Pipelines provisioned successfully.")

    // If the pipeline status in YAML is 'running', it will be started automatically.
    // If not, you can start it manually:
    // err = runtime.Orchestrator.Pipelines.Start(ctx, "my-embedded-pipeline")
    // if err != nil { /* ... handle error ... */ }
    ```

    `ProvisionService.Init(ctx)` will read the specified YAML file(s), create or update the pipelines in Conduit's internal state, and automatically start any pipelines marked with `status: running`.

### Option 2: Programmatic Pipeline Creation (Orchestrator)

For dynamic pipeline creation or more fine-grained control, you can use the `runtime.Orchestrator`.

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/rs/zerolog"
)

// newLogger is a helper function to create a Conduit-compatible logger.
func newLogger(level string, format string) log.CtxLogger {
	l, _ := zerolog.ParseLevel(level)
	f, _ := log.ParseFormat(format)
	return log.InitLogger(l, f)
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-signalChan:
			fmt.Println("\nInterrupt received, initiating graceful shutdown...")
			cancel()
		case <-ctx.Done():
		}
		<-signalChan
		fmt.Println("Second interrupt received, forcing exit.")
		os.Exit(2)
	}()

	cfg := conduit.DefaultConfig()
	cfg.DB.Type = conduit.DBTypeInMemory
	cfg.API.Enabled = false
	cfg.Log.Level = "debug"
	cfg.Log.Format = "cli"
	cfg.Log.NewLogger = newLogger

	runtime, err := conduit.NewRuntime(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize Conduit runtime: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := runtime.DB.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to close Conduit database: %v\n", err)
		}
	}()

	fmt.Println("Conduit runtime initialized.")

	// --- Programmatically create a pipeline ---
	pipelineID := "my-programmatic-pipeline"
	sourceID := "my-programmatic-source"
	destinationID := "my-programmatic-destination"

	fmt.Printf("Creating pipeline %s...\n", pipelineID)
	_, err = runtime.Orchestrator.Pipelines.Create(ctx, pipelineID, pipeline.Config{
		Name:        "My Programmatic Pipeline",
		Description: "A pipeline created programmatically",
	}, pipeline.ProvisionTypeAPI) // Indicate it's provisioned via API
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create pipeline: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Creating source connector %s...\n", sourceID)
	_, err = runtime.Orchestrator.Connectors.Create(ctx, sourceID, connector.TypeSource, "builtin:generator", pipelineID, connector.Config{
		Name: "Generator Source",
		Settings: map[string]string{
			"recordsPerSecond": "1",
			"value":            "hello programmatic world",
			"count":            "5", // Stop after 5 records
		},
	}, connector.ProvisionTypeAPI)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create source connector: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Creating destination connector %s...\n", destinationID)
	_, err = runtime.Orchestrator.Connectors.Create(ctx, destinationID, connector.TypeDestination, "builtin:log", pipelineID, connector.Config{
		Name: "Log Destination",
		Settings: map[string]string{},
	}, connector.ProvisionTypeAPI)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create destination connector: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Adding connectors to pipeline %s...\n", pipelineID)
	_, err = runtime.Orchestrator.Pipelines.AddConnector(ctx, pipelineID, sourceID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to add source to pipeline: %v\n", err)
		os.Exit(1)
	}
	_, err = runtime.Orchestrator.Pipelines.AddConnector(ctx, pipelineID, destinationID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to add destination to pipeline: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Starting pipeline %s...\n", pipelineID)
	err = runtime.Orchestrator.Pipelines.Start(ctx, pipelineID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start pipeline: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Pipeline created and started programmatically.")

	// ... continue with runtime.Run(ctx)
}
```

## Starting and Stopping the Runtime

After configuring and provisioning your pipelines, you need to start the Conduit runtime. This will kick off all internal goroutines responsible for running pipelines, processing data, and managing state.

The `runtime.Run(ctx)` method is blocking and will run until the provided context is canceled. It's crucial to handle graceful shutdowns when embedding Conduit.

```go
// In main() after all setup and provisioning:

fmt.Println("Starting Conduit main loop (will block until Ctrl+C)...")
err = runtime.Run(ctx)
if err != nil && !cerrors.Is(err, context.Canceled) {
	fmt.Fprintf(os.Stderr, "Conduit runtime error: %v\n", err)
	os.Exit(1)
}

fmt.Println("Conduit runtime stopped.")
// At this point, all pipelines have been stopped gracefully.
```

The example `examples/embedding/main.go` demonstrates the complete flow with a graceful shutdown mechanism using `context.WithCancel` and `os/signal`.

## Full Example

A complete, runnable example demonstrating embedding Conduit with file-based provisioning is available in the `examples/embedding` directory:

*   [`examples/embedding/main.go`](examples/embedding/main.go)
*   [`examples/embedding/pipeline.yaml`](examples/embedding/pipeline.yaml)

To run the example:

1.  Navigate to the `examples/embedding` directory.
2.  Run the Go application:
    ```bash
    go run main.go
    ```
3.  Observe the logs showing Conduit starting, the pipeline provisioning, and the generator source producing records to the log destination.
4.  Press `Ctrl+C` to initiate a graceful shutdown.

## Verification

To verify the implementation:

1.  **Navigate to the example directory:**
    ```bash
    cd examples/embedding
    ```

2.  **Run the embedding example:**
    ```bash
    go run main.go
    ```
    You should see output indicating:
    -   Conduit runtime initialization.
    -   Pipeline `my-embedded-pipeline` being provisioned and started.
    -   Records from the `generator` source being logged by the `log` destination (e.g., `value="hello world"`).
    -   After 5 records, the generator will stop, and the pipeline will eventually stop.

3.  **To test graceful shutdown, press `Ctrl+C` while the application is running.**
    You should see:
    -   "Interrupt received, initiating graceful shutdown..."
    -   "Conduit runtime stopped."
    -   "Application exiting."

4.  **Check the guide file exists:**
    ```bash
    ls -F docs/guides/embedding-conduit/index.md
    ```
    This command should print the path to the new guide file, confirming its creation.
