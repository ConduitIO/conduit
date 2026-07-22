# Package structure

- `cmd` - Contains main applications. The directory name for each application should match the name of the executable
  (e.g. `cmd/conduit` produces an executable called `conduit`). It is the responsibility of main applications to do 3
  things, it should not include anything else:
  1. Read the configuration (from a file, the environment or arguments).
  2. Instantiate, wire up and run internal services.
  3. Listen for signals (i.e. SIGTERM, SIGINT) and forward them to internal services to ensure a graceful shutdown
(e.g. via a closed context).
  - `conduit` - The entrypoint for the main Conduit executable.
- `pkg` - The internal libraries and services that Conduit runs.
  - `conduit` - Defines the main runtime that ties all Conduit layers together.
  - `connector` - Code regarding connectors, including connector store, connector service, connector configurations
      and running instances.
  - `foundation` - Foundation contains reusable code. Should not contain any business logic. A few honorable mentions:
    - `cerrors` - Exposes error creation and wrapping functionality. This is the only package for errors used in Conduit.
    - `ctxutil` - Utilities to operate with context.Context.
    - `grpcutil` - Utilities to operate with GRPC requests.
    - `log` - Exposes a logger. This is the logger used throughout Conduit.
    - `metrics` - Exposes functionality for gathering and exposing metrics.
  - `inspector` - Exposes a service that can be used to inspect and operate with the state of Conduit.
  - `orchestrator` - Code regarding the orchestration layer.
  - `pipeline` - Code regarding pipelines, including pipeline store, pipeline service, running pipeline instances.
  - `plugin` - Currently contains all logic related to plugins as well as the plugins themselves. In the future a lot of
      this code will be extracted into separate repositories, what will be left is a plugin service that manages built-in
      and external plugins.
  - `processor` - Code regarding processors, including processor store, processor service, running processor instances.
  - `provisioning` - Exposes a provisioning service that can be used to provision Conduit resources.
  - `schemaregistry` - Code regarding the schema registry.
  - `web` - Everything related to Conduit's hosted pages. Currently `web/ui`: the
      `go:embed`-ded, built-in web UI (observe + operate) served at `/` by `conduit run`,
      gated by `api.http.ui.enabled`. See
      `docs/design-documents/20260713-greenfield-built-in-ui.md` §7 and `pkg/conduit/ui.go`
      for the routing/disable contract. The Swagger UI (`pkg/http/openapi`) predates this
      convention and hasn't been moved here — don't invent a second location for a future
      hosted page; extend `web` instead.

Other folders that don't contain Go code:

- `docs` - Documentation regarding Conduit that's specific to this repository (more documentation can be found at [Conduit.io/docs](https://conduit.io/docs)).
- `proto` - Protobuf files (e.g. gRPC API definition).
- `scripts` - Contains scripts that are useful for development.
- `test` - Contains configurations needed for integration tests.

Note: there is no `ui/` subproject in this repository. The web UI's source lives in the
separate [`ConduitIO/conduit-ui`](https://github.com/ConduitIO/conduit-ui) repo (a greenfield
rebuild — see the design doc above); this repo only holds its _built_ output, embedded under
`pkg/web/ui/dist/` and reproduced with `make ui`.
