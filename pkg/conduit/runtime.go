// Copyright © 2022 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package conduit wires up everything under the hood of a Conduit instance
// including metrics, telemetry, logging, and server construction.
// It should only ever interact with the Orchestrator, never individual
// services. All of that responsibility should be left to the Orchestrator.
package conduit

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit-commons/database/badger"
	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/database/postgres"
	"github.com/conduitio/conduit-commons/database/sqlite"
	pconnutils "github.com/conduitio/conduit-connector-protocol/pconnutils/v1/server"
	connutilsv1 "github.com/conduitio/conduit-connector-protocol/proto/connutils/v1"
	conduitschemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/conduitio/conduit/pkg/conduit/dev"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/grpcutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
	"github.com/conduitio/conduit/pkg/foundation/metrics/prometheus"
	"github.com/conduitio/conduit/pkg/http/api"
	"github.com/conduitio/conduit/pkg/http/openapi"
	"github.com/conduitio/conduit/pkg/lifecycle"
	lifecycle_v2 "github.com/conduitio/conduit/pkg/lifecycle-poc"
	"github.com/conduitio/conduit/pkg/orchestrator"
	"github.com/conduitio/conduit/pkg/pipeline"
	conn_plugin "github.com/conduitio/conduit/pkg/plugin/connector"
	conn_builtin "github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/conduit/pkg/plugin/connector/connutils"
	conn_standalone "github.com/conduitio/conduit/pkg/plugin/connector/standalone"
	proc_plugin "github.com/conduitio/conduit/pkg/plugin/processor"
	proc_builtin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/conduitio/conduit/pkg/plugin/processor/procutils"
	proc_standalone "github.com/conduitio/conduit/pkg/plugin/processor/standalone"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	grpcruntime "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/piotrkowalczuk/promgrpc/v4"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/twmb/franz-go/pkg/sr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"gopkg.in/tomb.v2"
)

const (
	exitTimeout = 30 * time.Second
)

// Runtime sets up all services for serving and monitoring a Conduit instance.
type Runtime struct {
	Config Config

	DB               database.DB
	Orchestrator     *orchestrator.Orchestrator
	ProvisionService *provisioning.Service
	SchemaRegistry   schemaregistry.Registry

	// Ready will be closed when Runtime has successfully started
	Ready chan struct{}

	pipelineService  *pipeline.Service
	connectorService *connector.Service
	processorService *processor.Service
	lifecycleService lifecycleService

	connectorPluginService *conn_plugin.PluginService
	processorPluginService *proc_plugin.PluginService

	connSchemaService  *connutils.SchemaService
	connectorPersister *connector.Persister
	procSchemaService  *procutils.SchemaService

	logger log.CtxLogger

	// metricsGrpcStatsHandler is this Runtime's grpc stats handler, used by
	// serveGRPCAPI/startConnectorUtils. On the CLI path (no WithMetricsRegisterer
	// option) it is the process-wide singleton configureMetrics() returns,
	// exactly as before this field existed. On the embed path
	// (WithMetricsRegisterer set) it is a fresh, per-Runtime instance
	// registered only into the supplied registerer — see configureEmbeddedMetrics.
	metricsGrpcStatsHandler *promgrpc.StatsHandler

	// closeDBOnce/closeDBErr make CloseDB idempotent regardless of which of
	// its (potentially multiple) callers gets there first — see CloseDB's doc.
	closeDBOnce sync.Once
	closeDBErr  error
}

// CloseDB closes r.DB exactly once, no matter how many times or from how many
// call sites it is invoked, and returns the same error to every caller.
//
// This exists because r.DB is closed from two independent paths that must
// never race or double-close the same handle: registerCleanupV1/V2's
// tomb-driven drain (the normal `conduit run`/Handle.Stop shutdown path) and,
// for the embed API, Engine.Close's resource-release path, which must run
// regardless of whether Run was ever called or reached registerCleanup (see
// the root conduit package's Engine.Close doc). Routing both through this one
// sync.Once means neither path needs to know whether the other already ran.
func (r *Runtime) CloseDB() error {
	r.closeDBOnce.Do(func() {
		r.closeDBErr = r.DB.Close()
	})
	return r.closeDBErr
}

// runtimeOptions holds the optional embed-only seams threaded through
// NewRuntime via functional RuntimeOptions. The zero value matches
// `conduit run`'s exact behavior — every field here is additive: leaving it
// unset preserves what NewRuntime always did (AC-5: the seam is inert unless
// an embed option is passed; see TestNewRuntime_CLIDefaultsUnchanged).
type runtimeOptions struct {
	logger            *log.CtxLogger
	metricsRegisterer promclient.Registerer
	dialCtx           context.Context
}

// RuntimeOption configures an embed-only seam on NewRuntime. See WithLogger,
// WithMetricsRegisterer, WithDialContext.
type RuntimeOption func(*runtimeOptions)

// WithLogger supplies a pre-built log.CtxLogger, bypassing cfg.Log.NewLogger
// and log.GetWriter's os.Stdout default entirely. It also suppresses the
// process-global zerolog.DefaultContextLogger assignment NewRuntime otherwise
// makes: that global is process-wide and would cross-talk between two
// embedded Runtimes constructed with different loggers (see
// TestTwoEngines_LoggerIsolated) — the supplied logger is still attached
// directly to this Runtime and everything it constructs; only the ambient/
// fallback global is skipped. `conduit run` never passes this option, so its
// behavior (including the global assignment) is unchanged.
func WithLogger(logger log.CtxLogger) RuntimeOption {
	return func(o *runtimeOptions) { o.logger = &logger }
}

// WithMetricsRegisterer supplies a Prometheus registerer that receives this
// Runtime's metric families instead of the package-level configureMetrics()
// path (which registers into promclient.DefaultRegisterer behind a
// process-wide sync.Once, and is a no-op after the first call in the
// process). A Runtime constructed with this option gets its own
// foundation/metrics/prometheus.Registry and *promgrpc.StatsHandler,
// registered only into the supplied registerer — never
// promclient.DefaultRegisterer. `conduit run` never passes this option.
//
// Known limitation (documented, not fixed by this seam): pkg/foundation/metrics
// keeps process-global metric *definitions* (metrics.go's `global` slices) —
// a metric created via metrics.NewCounter et al. (e.g. everything in
// pkg/foundation/metrics/measure) fans out to every registry ever passed to
// metrics.Register, including a second embedded Runtime's. Two concurrently
// running embedded engines will therefore observe each other's metric
// *values* even though each has its own isolated Registerer/Registry
// *object*. See TestTwoEngines_MetricsCrossTalk_KnownLimitation — fixing this
// is tracked as a follow-up, not this workstream (see the embed design doc's
// Non-goals).
func WithMetricsRegisterer(reg promclient.Registerer) RuntimeOption {
	return func(o *runtimeOptions) { o.metricsRegisterer = reg }
}

// WithDialContext supplies the context.Context OpenStore's Postgres/SQLite
// dial uses instead of context.Background(), so a caller-supplied deadline or
// cancellation aborts a slow/unreachable database dial promptly instead of
// blocking indefinitely. It has no effect on the Badger/InMemory DB types
// (they never dial a remote/blocking connection). It is not retained beyond
// NewRuntime's own call — Runtime.Run's context is independent and must be
// supplied fresh to Run (do not store a ctx across calls).
func WithDialContext(ctx context.Context) RuntimeOption {
	return func(o *runtimeOptions) { o.dialCtx = ctx }
}

// lifecycleService is an interface that we use temporarily to allow for
// both the old and new lifecycle services to be used interchangeably.
type lifecycleService interface {
	Start(ctx context.Context, pipelineID string) error
	Stop(ctx context.Context, pipelineID string, force bool) error
	// StopAndWait is required so this interface stays a superset of
	// provisioning.LifecycleService (Go requires that for the implicit
	// interface-to-interface assignment in newRuntime below to type-check).
	// See lifecycle.Service.StopAndWait's doc for what it guarantees, and
	// lifecycle-poc(pkg/lifecycle-poc).Service.StopAndWait for why the
	// Preview.PipelineArchV2 implementation always refuses.
	StopAndWait(ctx context.Context, pipelineID string) error
	// ReconfigureProcessor keeps this interface a superset of
	// provisioning.LifecycleService (see StopAndWait above) — it is used by the
	// live in-place apply path. See lifecycle.Service.ReconfigureProcessor.
	ReconfigureProcessor(ctx context.Context, pipelineID, processorID string) error
	Init(ctx context.Context) error
}

// NewRuntime sets up a Runtime instance and primes it for start. opts are
// additive, embed-only seams (see RuntimeOption); called with none, behavior
// is byte-for-byte what NewRuntime did before opts existed — this is what
// `conduit run` (via Entrypoint.Serve) always does.
func NewRuntime(cfg Config, opts ...RuntimeOption) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, cerrors.Errorf("invalid config: %w", err)
	}

	var ro runtimeOptions
	for _, opt := range opts {
		opt(&ro)
	}

	var logger log.CtxLogger
	if ro.logger != nil {
		// Embed path (AC-5.1): use the pre-built logger as-is instead of
		// cfg.Log.NewLogger/log.GetWriter's os.Stdout default.
		logger = *ro.logger
	} else {
		logger = cfg.Log.NewLogger(cfg.Log.Level, cfg.Log.Format)
	}
	logger.Logger = logger.
		Hook(ctxutil.MessageIDLogCtxHook{}).
		Hook(ctxutil.RequestIDLogCtxHook{}).
		Hook(ctxutil.FilepathLogCtxHook{})
	if ro.logger == nil {
		// CLI path only (AC-5.3): an embed-supplied logger must never become
		// the ambient global fallback — see WithLogger's doc.
		zerolog.DefaultContextLogger = &logger.Logger
	}

	dialCtx := context.Background()
	if ro.dialCtx != nil {
		// AC-5.4: let an embedder bound/cancel the store dial.
		dialCtx = ro.dialCtx
	}

	db, err := OpenStore(dialCtx, cfg, logger)
	if err != nil {
		return nil, err
	}

	var statsHandler *promgrpc.StatsHandler
	if ro.metricsRegisterer != nil {
		// Embed path (AC-5.2): isolated per-Runtime metrics, never the
		// process-global promclient.DefaultRegisterer.
		statsHandler, err = configureEmbeddedMetrics(ro.metricsRegisterer)
		if err != nil {
			return nil, err
		}
	} else {
		statsHandler = configureMetrics()
	}
	measure.ConduitInfo.WithValues(Version(true)).Inc()

	// Start the connector persister
	connectorPersister := connector.NewPersister(logger, db,
		connector.DefaultPersisterDelayThreshold,
		connector.DefaultPersisterBundleCountThreshold,
	)

	r := &Runtime{
		Config: cfg,
		DB:     db,
		Ready:  make(chan struct{}),

		connectorPersister: connectorPersister,

		logger:                  logger,
		metricsGrpcStatsHandler: statsHandler,
	}

	err = createServices(r)
	if err != nil {
		return nil, cerrors.Errorf("failed to initialize services: %w", err)
	}

	return r, nil
}

// OpenStore opens the database driver configured by cfg (or returns
// cfg.DB.Driver directly, when the embedder set one explicitly) without
// doing anything else — no metrics, no persister, no services. It is the
// exact DB-open logic NewRuntime uses, extracted so `conduit doctor`'s
// store.reachable check (see
// docs/design-documents/20260707-cli-doctor.md) can probe database
// reachability the same way `conduit run` would, instead of reimplementing
// or drifting from it.
//
// ctx bounds the Postgres/SQLite dial only (AC-5.4) — canceling it aborts a
// slow/unreachable dial instead of blocking indefinitely. It has no effect on
// Badger/InMemory (neither dials). ctx is not retained past this call.
//
// logger is a required parameter (not derived internally from cfg.Log) so a
// caller that only wants to probe reachability — doctor runs before any
// Runtime exists — can supply a throwaway logger (e.g. log.Nop()) instead of
// depending on Runtime construction order or Config.Log.NewLogger being set.
//
// The returned database.DB is opened but not pinged; callers that only want
// to validate config (not actually dial/open anything) should not call this.
// A caller that opens a store here is responsible for calling Close on it.
func OpenStore(ctx context.Context, cfg Config, logger log.CtxLogger) (database.DB, error) {
	if cfg.DB.Driver != nil {
		return cfg.DB.Driver, nil
	}

	var db database.DB
	var err error
	switch cfg.DB.Type {
	case DBTypeBadger:
		db, err = badger.New(logger.Logger, cfg.DB.Badger.Path)
	case DBTypePostgres:
		db, err = postgres.New(ctx, logger.Logger, cfg.DB.Postgres.ConnectionString, cfg.DB.Postgres.Table)
	case DBTypeInMemory:
		db = &inmemory.DB{}
		logger.Warn(context.Background()).Msg("Using in-memory store, all pipeline configurations will be lost when Conduit stops.")
	case DBTypeSQLite:
		db, err = sqlite.New(ctx, logger.Logger, cfg.DB.SQLite.Path, cfg.DB.SQLite.Table)
	default:
		// An unsupported DB type is a config/validation problem, not an
		// environment one. It stays exit 1 (the runtime default for an
		// untagged error) by design — return it directly instead of falling into
		// the Unavailable tagging below, which is reserved for a
		// genuinely unreachable database (connection refused, file
		// locked/inaccessible, etc.) for the configured, valid type.
		return nil, cerrors.Errorf("invalid DB type %q", cfg.DB.Type)
	}
	if err != nil {
		// Pass the wrapped error (not the raw err) as Wrap's cause so the
		// stack frame cerrors.Errorf captures here isn't discarded:
		// conduiterr.Wrap only synthesizes a frame of its own when cause
		// is nil, and the raw driver error typically carries none.
		wrapped := cerrors.Errorf("failed to create a DB instance: %w", err)
		// Tagged Unavailable: the database Conduit depends on could not
		// be opened/dialed. pkg/conduit/exitcode maps this to exit code
		// 3 (environment), distinct from a config/validation failure.
		return nil, conduiterr.Wrap(conduiterr.CodeUnavailable, wrapped.Error(), wrapped)
	}
	return db, nil
}

// Create all necessary internal services
func createServices(r *Runtime) error {
	schemaRegistry, err := createSchemaRegistry(r.Config, r.logger, r.DB)
	if err != nil {
		return cerrors.Errorf("failed to create schema registry: %w", err)
	}

	procSchemaService := procutils.NewSchemaService(r.logger, schemaRegistry)
	standaloneReg, err := proc_standalone.NewRegistry(r.logger, r.Config.Processors.Path, procSchemaService)
	if err != nil {
		return cerrors.Errorf("failed creating processor registry: %w", err)
	}

	procPluginService := proc_plugin.NewPluginService(
		r.logger,
		proc_builtin.NewRegistry(r.logger, r.Config.ProcessorPlugins, schemaRegistry),
		standaloneReg,
	)

	tokenService := connutils.NewAuthManager()
	connSchemaService := connutils.NewSchemaService(r.logger, schemaRegistry, tokenService)

	connPluginService := conn_plugin.NewPluginService(
		r.logger,
		conn_builtin.NewRegistry(
			r.logger,
			r.Config.ConnectorPlugins,
			connSchemaService,
		),
		conn_standalone.NewRegistry(r.logger, r.Config.Connectors.Path),
		tokenService,
	)

	plService := pipeline.NewService(r.logger, r.DB)
	connService := connector.NewService(r.logger, r.DB, r.connectorPersister)
	procService := processor.NewService(r.logger, r.DB, procPluginService)

	var lifecycleService lifecycleService
	if r.Config.Preview.PipelineArchV2 {
		r.logger.Info(context.Background()).Msg("using lifecycle service v2")
		lifecycleService = lifecycle_v2.NewService(
			r.logger,
			connService,
			procService,
			connPluginService,
			plService,
			r.Config.Preview.PipelineArchV2DisableMetrics,
		)
	} else {
		// Error recovery configuration
		errRecoveryCfg := &lifecycle.ErrRecoveryCfg{
			MinDelay:         r.Config.Pipelines.ErrorRecovery.MinDelay,
			MaxDelay:         r.Config.Pipelines.ErrorRecovery.MaxDelay,
			BackoffFactor:    r.Config.Pipelines.ErrorRecovery.BackoffFactor,
			MaxRetries:       r.Config.Pipelines.ErrorRecovery.MaxRetries,
			MaxRetriesWindow: r.Config.Pipelines.ErrorRecovery.MaxRetriesWindow,
		}

		lifecycleService = lifecycle.NewService(r.logger, errRecoveryCfg, connService, procService, connPluginService, plService)
	}

	provisionService := provisioning.NewService(r.DB, r.logger, plService, connService, procService, connPluginService, lifecycleService, r.Config.Pipelines.Path)
	orc := orchestrator.NewOrchestrator(r.DB, r.logger, plService, connService, procService, connPluginService, procPluginService, lifecycleService)

	r.Orchestrator = orc
	r.ProvisionService = provisionService
	r.SchemaRegistry = schemaRegistry

	r.pipelineService = plService
	r.connectorService = connService
	r.processorService = procService
	r.connectorPluginService = connPluginService
	r.processorPluginService = procPluginService
	r.connSchemaService = connSchemaService
	r.procSchemaService = procSchemaService
	r.lifecycleService = lifecycleService

	return nil
}

func createSchemaRegistry(config Config, logger log.CtxLogger, db database.DB) (schemaregistry.Registry, error) {
	var schemaRegistry schemaregistry.Registry
	var err error

	switch config.SchemaRegistry.Type {
	case SchemaRegistryTypeConfluent:
		opts := []sr.ClientOpt{
			sr.URLs(config.SchemaRegistry.Confluent.ConnectionString),
		}
		// Basic Auth
		if config.SchemaRegistry.Confluent.Authentication.Type == SchemaRegistryAuthTypeBasic {
			opts = append(opts, sr.BasicAuth(
				config.SchemaRegistry.Confluent.Authentication.Username,
				config.SchemaRegistry.Confluent.Authentication.Password,
			))
		}
		// Bearer Auth
		if config.SchemaRegistry.Confluent.Authentication.Type == SchemaRegistryAuthTypeBearer {
			opts = append(opts, sr.BearerToken(config.SchemaRegistry.Confluent.Authentication.Token))
		}
		schemaRegistry, err = schemaregistry.NewClient(logger, opts...)
		if err != nil {
			return nil, cerrors.Errorf("failed to create schema registry client: %w", err)
		}
	case SchemaRegistryTypeBuiltin:
		schemaRegistry, err = conduitschemaregistry.NewSchemaRegistry(db)
		if err != nil {
			return nil, cerrors.Errorf("failed to create built-in schema registry: %w", err)
		}
	default:
		// shouldn't happen, we validate the config
		return nil, cerrors.Errorf("invalid schema registry type %q", config.SchemaRegistry.Type)
	}

	return schemaRegistry, nil
}

func newLogger(level string, format string) log.CtxLogger {
	// TODO make logger hooks configurable
	l, _ := zerolog.ParseLevel(level)
	f, _ := log.ParseFormat(format)
	return log.InitLogger(l, f)
}

var (
	metricsConfigureOnce    sync.Once
	metricsGrpcStatsHandler *promgrpc.StatsHandler
)

// configureMetrics
func configureMetrics() *promgrpc.StatsHandler {
	metricsConfigureOnce.Do(func() {
		// conduit metrics
		reg := prometheus.NewRegistry(nil)
		metrics.Register(reg)
		promclient.MustRegister(reg)

		// grpc metrics
		metricsGrpcStatsHandler = promgrpc.ServerStatsHandler()
		promclient.MustRegister(metricsGrpcStatsHandler)
	})
	return metricsGrpcStatsHandler
}

// configureEmbeddedMetrics is configureMetrics' embed-path counterpart (see
// WithMetricsRegisterer): it builds a fresh, per-Runtime
// foundation/metrics/prometheus.Registry and *promgrpc.StatsHandler and
// registers both only into registerer — never promclient.DefaultRegisterer,
// and never behind the CLI path's process-wide metricsConfigureOnce (each
// call here produces an independent StatsHandler instance, so two embedded
// Runtimes never share one).
//
// It uses registerer.Register (which returns an error) rather than
// MustRegister (which panics) so a metric-name collision on a registerer the
// caller reused across engines surfaces as a coded error from New, per
// invariant: New never panics on a caller-supplied misconfiguration.
//
// Known limitation, accepted for v1 and tracked as
// https://github.com/ConduitIO/conduit/issues/2669: metrics.Register(reg)
// below runs before either registerer.Register call below is known to
// succeed, so a failed Register (e.g. the name collision this function
// itself guards against) still leaks reg into pkg/foundation/metrics'
// process-global bookkeeping — it is never unregistered on this error path.
func configureEmbeddedMetrics(registerer promclient.Registerer) (*promgrpc.StatsHandler, error) {
	reg := prometheus.NewRegistry(nil)
	metrics.Register(reg) // documented cross-talk limitation, see WithMetricsRegisterer's doc; leak-on-failure tracked as issue #2669
	if err := registerer.Register(reg); err != nil {
		wrapped := cerrors.Errorf("failed to register conduit metrics collector: %w", err)
		return nil, conduiterr.Wrap(conduiterr.CodeInvalidArgument, wrapped.Error(), wrapped)
	}

	statsHandler := promgrpc.ServerStatsHandler()
	if err := registerer.Register(statsHandler); err != nil {
		wrapped := cerrors.Errorf("failed to register grpc stats collector: %w", err)
		return nil, conduiterr.Wrap(conduiterr.CodeInvalidArgument, wrapped.Error(), wrapped)
	}
	return statsHandler, nil
}

// Run initializes all of Conduit's underlying services and starts the GRPC and
// HTTP APIs. This function blocks until the supplied context is cancelled or
// one of the services experiences a fatal error.
func (r *Runtime) Run(ctx context.Context) (err error) {
	// Tier-1-caution guard (embed workstream plan §11 task 6): if ctx is
	// already canceled before Run does any work, return immediately instead
	// of spinning up profiling, the tomb, and every service. This is not
	// merely an optimization: empirically, without this guard, Run panics
	// ("tomb.Go called after all goroutines terminated") when handed an
	// already-canceled context — tomb.WithContext's own cancellation-watcher
	// goroutine kills the tomb (and lets it finish) before initServices's
	// later t.Go calls (e.g. serveGRPC) run, so those calls hit an
	// already-dead tomb. `conduit run` never calls Run with an
	// already-canceled context (a live process's context is only canceled by
	// a signal that arrives after Run is already executing), so this is
	// inert on the CLI path; it matters for the embed calling convention
	// (Engine.Run), where a host can legitimately pass an already-canceled or
	// already-expired ctx (e.g. one derived from an expired request
	// deadline) — a documented, designed-for input, not caller misuse. This
	// does not touch registerCleanup/the tomb's drain mechanics — it is a
	// pure early return before either is constructed; the DB opened by
	// NewRuntime is still live afterward, which is exactly why the embed
	// API's Engine.Close exists as a resource-release path independent of
	// whether Run got this far (see the root conduit package's Engine.Close
	// doc).
	//
	// The error is returned through conduiterr.CodeInvalidArgument (not a
	// bare context.Canceled/DeadlineExceeded) so it satisfies New/Run's
	// documented promise of a *conduiterr.ConduitError where classifiable.
	// Reusing CodeInvalidArgument rather than minting a new code matches this
	// package's existing convention for embed-lifecycle-contract errors (see
	// the root conduit package's Engine.Run/Handle.Stop docs for the same
	// reasoning applied to a double Run call and a Stop timeout).
	if err := ctx.Err(); err != nil {
		wrapped := cerrors.Errorf("conduit: Run called with an already-done context: %w", err)
		return conduiterr.Wrap(conduiterr.CodeInvalidArgument, wrapped.Error(), wrapped)
	}

	cleanup, err := r.initProfiling(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	t, ctx := tomb.WithContext(ctx)

	defer func() {
		if err != nil {
			// This means run failed, we kill the tomb to stop any goroutines
			// that might have been already started.
			t.Kill(err)
		}
		// Block until tomb is dying, then wait for goroutines to stop running.
		<-t.Dying()
		r.logger.Warn(ctx).Msg("conduit is stopping, stand by for shutdown ...")
		err = t.Wait()
	}()

	// Register cleanup function that will run after tomb is killed
	r.registerCleanup(t)

	// Initialize all services
	err = r.initServices(ctx, t)
	if err != nil {
		return cerrors.Errorf("failed to initialize services: %w", err)
	}

	// Public gRPC and HTTP API
	if r.Config.API.Enabled {
		// Serve grpc and http API
		grpcAddr, err := r.serveGRPCAPI(ctx, t)
		if err != nil {
			return cerrors.Errorf("failed to serve grpc api: %w", err)
		}
		httpAddr, err := r.serveHTTPAPI(ctx, t, grpcAddr)
		if err != nil {
			return cerrors.Errorf("failed to serve http api: %w", err)
		}

		port := 8080 // default
		if tcpAddr, ok := httpAddr.(*net.TCPAddr); ok {
			port = tcpAddr.Port
		}
		r.logger.Info(ctx).Send()
		r.logger.Info(ctx).Msgf("click here to navigate to explore the HTTP API: http://localhost:%d/openapi", port)
		r.logger.Info(ctx).Send()
	} else {
		r.logger.Info(ctx).Msg("API is disabled")
	}

	close(r.Ready)
	return nil
}

func (r *Runtime) initProfiling(ctx context.Context) (deferred func(), err error) {
	deferred = func() {}

	// deferFunc adds the func into deferred so it can be executed by the caller
	// in a defer statement
	deferFunc := func(f func()) {
		oldDeferred := deferred
		deferred = func() {
			f()
			oldDeferred()
		}
	}
	// ignoreErr returns a function that executes f and ignores the returned error
	ignoreErr := func(f func() error) func() {
		return func() {
			_ = f() // ignore error
		}
	}
	defer func() {
		if err != nil {
			// on error we make sure deferred functions are executed and return
			// an empty function as deferred instead
			deferred()
			deferred = func() {}
		}
	}()

	if r.Config.Dev.CPUProfile != "" {
		f, err := os.Create(r.Config.Dev.CPUProfile)
		if err != nil {
			return deferred, cerrors.Errorf("could not create CPU profile: %w", err)
		}
		deferFunc(ignoreErr(f.Close))
		if err := pprof.StartCPUProfile(f); err != nil {
			return deferred, cerrors.Errorf("could not start CPU profile: %w", err)
		}
		deferFunc(pprof.StopCPUProfile)
	}
	if r.Config.Dev.MemProfile != "" {
		deferFunc(func() {
			f, err := os.Create(r.Config.Dev.MemProfile)
			if err != nil {
				r.logger.Err(ctx, err).Msg("could not create memory profile")
				return
			}
			defer f.Close()
			runtime.GC() // get up-to-date statistics
			if err := pprof.WriteHeapProfile(f); err != nil {
				r.logger.Err(ctx, err).Msg("could not write memory profile")
			}
		})
	}
	if r.Config.Dev.BlockProfile != "" {
		runtime.SetBlockProfileRate(1)
		deferFunc(func() {
			f, err := os.Create(r.Config.Dev.BlockProfile)
			if err != nil {
				r.logger.Err(ctx, err).Msg("could not create block profile")
				return
			}
			defer f.Close()
			if err := pprof.Lookup("block").WriteTo(f, 0); err != nil {
				r.logger.Err(ctx, err).Msg("could not write block profile")
			}
		})
	}
	return
}

func (r *Runtime) registerCleanup(t *tomb.Tomb) {
	if r.Config.Preview.PipelineArchV2 {
		r.registerCleanupV2(t)
	} else {
		r.registerCleanupV1(t)
	}
}

func (r *Runtime) registerCleanupV1(t *tomb.Tomb) {
	ls := r.lifecycleService.(*lifecycle.Service)
	t.Go(func() error {
		<-t.Dying()
		// start cleanup with a fresh context
		ctx := context.Background()

		// t.Err() can be nil, when we had a call: t.Kill(nil)
		// t.Err() will be context.Canceled, if the tomb's context was canceled
		if t.Err() == nil || cerrors.Is(t.Err(), context.Canceled) {
			ls.StopAll(ctx, pipeline.ErrGracefulShutdown)
		} else {
			// tomb died due to a real error
			ls.StopAll(ctx, cerrors.Errorf("conduit experienced an error: %w", t.Err()))
		}
		err := ls.Wait(exitTimeout)
		t.Go(func() error {
			r.connectorPersister.Wait()
			return r.CloseDB()
		})
		return err
	})
}

func (r *Runtime) registerCleanupV2(t *tomb.Tomb) {
	ls := r.lifecycleService.(*lifecycle_v2.Service)
	t.Go(func() error {
		<-t.Dying()
		// start cleanup with a fresh context
		ctx := context.Background()

		err := ls.StopAll(ctx, false)
		if err != nil {
			r.logger.Err(ctx, err).Msg("some pipelines stopped with an error")
		}

		// Wait for the pipelines to stop
		const (
			count    = 6
			interval = exitTimeout / count
		)

		pipelinesStopped := make(chan struct{})
		go func() {
			for i := count; i > 0; i-- {
				if i == 1 {
					// on last try, stop forcefully
					_ = ls.StopAll(ctx, true)
				}

				r.logger.Info(ctx).Msgf("waiting for pipelines to stop running (time left: %s)", time.Duration(i)*interval)
				select {
				case <-time.After(interval):
				case <-pipelinesStopped:
					return
				}
			}
		}()

		err = ls.Wait(exitTimeout)
		switch {
		case err != nil && err != context.DeadlineExceeded:
			r.logger.Warn(ctx).Err(err).Msg("some pipelines stopped with an error")
		case err == context.DeadlineExceeded:
			r.logger.Warn(ctx).Msg("some pipelines did not stop in time")
		default:
			r.logger.Info(ctx).Msg("all pipelines stopped gracefully")
		}

		pipelinesStopped <- struct{}{}

		t.Go(func() error {
			r.connectorPersister.Wait()
			return r.CloseDB()
		})

		return nil
	})
}

// newHTTPMetricsHandler builds the handler served at /metrics (see
// serveHTTPAPI).
//
// Known limitation, accepted for v1 and tracked as
// https://github.com/ConduitIO/conduit/issues/2670: promhttp.Handler() always
// serves promclient.DefaultGatherer, not the Registerer/Gatherer an embedder
// supplied via WithMetricsRegisterer/configureEmbeddedMetrics. An embedded
// Runtime with Options.API.Enabled and a custom MetricsRegisterer therefore
// gets a /metrics route that does not reflect what was actually registered
// into that registerer.
func (r *Runtime) newHTTPMetricsHandler() http.Handler {
	return promhttp.Handler()
}

func (r *Runtime) serveGRPCAPI(ctx context.Context, t *tomb.Tomb) (net.Addr, error) {
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpcutil.RequestIDUnaryServerInterceptor(r.logger),
			grpcutil.LoggerUnaryServerInterceptor(r.logger),
		),
		grpc.StatsHandler(r.metricsGrpcStatsHandler),
		grpc.MaxRecvMsgSize(10*1024*1024),
	)

	pipelineAPIv1 := api.NewPipelineAPIv1(r.Orchestrator.Pipelines, r.ProvisionService, r.Config.API.AllowLiveRestartApply)
	pipelineAPIv1.Register(grpcServer)

	processorAPIv1 := api.NewProcessorAPIv1(r.Orchestrator.Processors, r.Orchestrator.ProcessorPlugins)
	processorAPIv1.Register(grpcServer)

	connectorAPIv1 := api.NewConnectorAPIv1(r.Orchestrator.Connectors, r.Orchestrator.ConnectorPlugins)
	connectorAPIv1.Register(grpcServer)

	pluginAPIv1 := api.NewPluginAPIv1(r.Orchestrator.ConnectorPlugins)
	pluginAPIv1.Register(grpcServer)

	info := api.NewInformation(Version(false))
	info.Register(grpcServer)
	// Makes it easier to use command line tools to interact
	// with the gRPC API.
	// https://github.com/grpc/grpc/blob/master/doc/server-reflection.md
	reflection.Register(grpcServer)

	// Names taken from api.proto
	healthServer := api.NewHealthServer(
		map[string]api.Checker{
			"PipelineService":        r.pipelineService,
			"ConnectorService":       r.connectorService,
			"ProcessorService":       r.processorService,
			"ConnectorPluginService": r.connectorPluginService,
			"ProcessorPluginService": r.processorPluginService,
		},
		r.logger,
	)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)

	// serve grpc server
	addr, err := r.serveGRPC(ctx, t, grpcServer, r.Config.API.GRPC.Address)
	if err != nil {
		return nil, err
	}

	r.logger.Info(ctx).Str(log.ServerAddressField, addr.String()).Msg("grpc API started")
	return addr, nil
}

// startConnectorUtils starts all the utility services needed by connectors.
func (r *Runtime) startConnectorUtils(ctx context.Context, t *tomb.Tomb) (net.Addr, error) {
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpcutil.RequestIDUnaryServerInterceptor(r.logger),
			grpcutil.LoggerUnaryServerInterceptor(r.logger),
		),
		grpc.StatsHandler(r.metricsGrpcStatsHandler),
	)

	schemaServiceAPI := pconnutils.NewSchemaServiceServer(r.connSchemaService)
	connutilsv1.RegisterSchemaServiceServer(grpcServer, schemaServiceAPI)

	// Makes it easier to use command line tools to interact
	// with the gRPC API.
	// https://github.com/grpc/grpc/blob/master/doc/server-reflection.md
	reflection.Register(grpcServer)

	// Names taken from schema.proto
	healthServer := api.NewHealthServer(
		map[string]api.Checker{
			"SchemaService": r.connSchemaService,
		},
		r.logger,
	)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)

	// Serve utilities on a random port
	addr, err := r.serveGRPC(ctx, t, grpcServer, ":0")
	if err != nil {
		return nil, err
	}

	r.logger.Info(ctx).Str(log.ServerAddressField, addr.String()).Msg("connector utilities started")
	return addr, nil
}

func (r *Runtime) serveHTTPAPI(
	ctx context.Context,
	t *tomb.Tomb,
	grpcAddr net.Addr,
) (net.Addr, error) {
	conn, err := grpc.NewClient(grpcAddr.String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, cerrors.Errorf("failed to dial server: %w", err)
	}

	gwmux := grpcruntime.NewServeMux(
		grpcruntime.WithIncomingHeaderMatcher(grpcutil.HeaderMatcher),
		grpcruntime.WithOutgoingHeaderMatcher(grpcutil.HeaderMatcher),
		grpcutil.WithErrorHandler(r.logger),
		grpcutil.WithPrettyJSONMarshaler(),
		grpcruntime.WithHealthzEndpoint(grpc_health_v1.NewHealthClient(conn)),
	)

	err = apiv1.RegisterPipelineServiceHandler(ctx, gwmux, conn)
	if err != nil {
		return nil, cerrors.Errorf("failed to register pipelines handler: %w", err)
	}

	err = apiv1.RegisterConnectorServiceHandler(ctx, gwmux, conn)
	if err != nil {
		return nil, cerrors.Errorf("failed to register connectors handler: %w", err)
	}

	err = apiv1.RegisterProcessorServiceHandler(ctx, gwmux, conn)
	if err != nil {
		return nil, cerrors.Errorf("failed to register processors handler: %w", err)
	}

	err = apiv1.RegisterPluginServiceHandler(ctx, gwmux, conn)
	if err != nil {
		return nil, cerrors.Errorf("failed to register plugins handler: %w", err)
	}

	err = apiv1.RegisterInformationServiceHandler(ctx, gwmux, conn)
	if err != nil {
		return nil, cerrors.Errorf("failed to register Information handler: %w", err)
	}

	oaHandler := http.StripPrefix("/openapi/", openapi.Handler())
	err = gwmux.HandlePath(
		"GET",
		"/openapi/**",
		func(w http.ResponseWriter, req *http.Request, pathParams map[string]string) {
			oaHandler.ServeHTTP(w, req)
		},
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to register openapi handler: %w", err)
	}

	err = gwmux.HandlePath(
		"GET",
		"/openapi",
		func(w http.ResponseWriter, req *http.Request, pathParams map[string]string) {
			http.Redirect(w, req, "/openapi/", http.StatusFound)
		},
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to register openapi redirect handler: %w", err)
	}

	metricsHandler := r.newHTTPMetricsHandler()
	err = gwmux.HandlePath(
		"GET",
		"/metrics",
		func(w http.ResponseWriter, req *http.Request, pathParams map[string]string) {
			metricsHandler.ServeHTTP(w, req)
		},
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to register metrics handler: %w", err)
	}

	readyzHandler := r.readyzHandler()
	err = gwmux.HandlePath(
		"GET",
		"/readyz",
		func(w http.ResponseWriter, req *http.Request, pathParams map[string]string) {
			readyzHandler.ServeHTTP(w, req)
		},
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to register readyz handler: %w", err)
	}

	allowedOrigins := r.Config.API.HTTP.CORS.AllowedOrigins
	if slices.Contains(allowedOrigins, "*") {
		// The API has no authentication. A wildcard CORS origin therefore lets any
		// web page reach it; combined with a non-loopback bind, that is
		// network-wide unauthenticated read and control. Warn loudly, louder still
		// when the bind is not loopback.
		w := r.logger.Warn(ctx)
		if isLoopbackBind(r.Config.API.HTTP.Address) {
			w.Msg("CORS is configured with wildcard '*': any web origin may call the unauthenticated HTTP API and websocket streams. Prefer exact origins outside local development.")
		} else {
			w.Str(log.ServerAddressField, r.Config.API.HTTP.Address).
				Msg("CORS wildcard '*' with a non-loopback bind and no API authentication: any website in any browser on this network can read and control this Conduit instance. Use exact origins, bind to loopback, or front it with an authenticating proxy.")
		}
	}
	handler := buildAPIHandler(ctx, gwmux, allowedOrigins, r.logger)
	// UI-7: mount the embedded built-in UI at "/", last, so it can never
	// shadow any route above (see pkg/conduit/ui.go and
	// docs/design-documents/20260713-greenfield-built-in-ui.md §7).
	handler = uiMiddleware(ctx, handler, r.Config.API.HTTP.UI.Enabled, r.logger)

	addr, err := r.serveHTTP(
		ctx,
		t,
		&http.Server{
			Addr:              r.Config.API.HTTP.Address,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		},
	)
	if err != nil {
		return nil, err
	}

	r.logger.Info(ctx).Str(log.ServerAddressField, addr.String()).Msg("http API started")
	return addr, nil
}

func preflightHandler(w http.ResponseWriter, r *http.Request) {
	// The origin is already known-allowed by allowCORS before this runs. Reflect
	// the browser's requested headers (rather than a fixed list) so a UI setting a
	// correlation header (x-request-id, read by the gateway) or, in future, an
	// Authorization header is not blocked by an out-of-date allowlist. Fall back to
	// the historical minimal set when the browser requests nothing specific.
	reqHeaders := r.Header.Get("Access-Control-Request-Headers")
	if reqHeaders == "" {
		reqHeaders = "Content-Type,Accept"
	}
	w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
	w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,POST,PUT,DELETE")
	// Let the browser cache the preflight so it doesn't re-issue OPTIONS before
	// every API/stream call.
	w.Header().Set("Access-Control-Max-Age", "600")
}

// buildAPIHandler wraps the gateway mux with CORS and websocket proxying, both
// driven by the same origin allowlist, exactly as serveHTTPAPI assembles it.
// Extracted so the wiring — that allowCORS AND the websocket upgrader's
// CheckOrigin are both applied from the same allowlist — is testable without
// standing up the full runtime (a regression that dropped wsCheckOrigin here
// would otherwise pass every unit test).
func buildAPIHandler(ctx context.Context, gwmux http.Handler, allowedOrigins []string, logger log.CtxLogger) http.Handler {
	return grpcutil.WithWebsockets(
		ctx,
		grpcutil.WithDefaultGatewayMiddleware(allowCORS(gwmux, allowedOrigins)),
		logger,
		wsCheckOrigin(allowedOrigins),
	)
}

// originAllowed reports whether origin is permitted by the configured allowlist:
// true if the list contains the wildcard "*" or the exact origin. It is the
// single origin-decision shared by allowCORS (HTTP) and the websocket CheckOrigin,
// so the two surfaces can never diverge.
func originAllowed(origin string, allowedOrigins []string) bool {
	for _, a := range allowedOrigins {
		if a == "*" || a == origin {
			return true
		}
	}
	return false
}

// allowCORS enables Cross-Origin Resource Sharing for the browser origins in
// allowedOrigins (exact match, or all when the list contains "*"). It reflects the
// matched origin (never a literal "*") and sets Vary: Origin so a shared cache
// can't serve one origin's headers to another. It sets no Access-Control-Allow-
// Credentials: the API is unauthenticated, and reflecting the origin keeps this
// forward-safe if auth is ever added. An empty allowlist denies all cross-origin
// requests (the secure default); same-origin requests are unaffected either way.
func allowCORS(h http.Handler, allowedOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && originAllowed(origin, allowedOrigins) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				preflightHandler(w, r)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// wsCheckOrigin builds the websocket upgrader's origin guard: gorilla's
// same-origin default as a floor, PLUS the configured cross-origin allowlist.
// HTTP CORS middleware never runs on a websocket-upgrade request (webSocketProxy
// intercepts it first), so the upgrader needs its own check. Browsers send an
// Origin header even on SAME-origin websocket handshakes, so a bare allowlist
// check would reject the same-origin embedded UI under the default (empty)
// allowlist — hence the explicit same-origin allowance, which also preserves the
// pre-change behavior (gorilla's unset CheckOrigin allowed same-origin). A
// request with no Origin (curl / CLI / non-browser) is allowed, as gorilla does.
func wsCheckOrigin(allowedOrigins []string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		// Same-origin: Origin's host:port equals the request Host. This is
		// gorilla's own default and must hold regardless of the allowlist. The
		// u.Host != "" guard is defense-in-depth: r.Host is never empty at a real
		// handler (Go rejects HTTP/1.1 without Host), but this ensures an
		// unparseable/host-less Origin can never match an empty Host.
		if u, err := url.Parse(origin); err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host) {
			return true
		}
		return originAllowed(origin, allowedOrigins)
	}
}

// isLoopbackBind reports whether addr binds only the loopback interface. An empty
// host (":8080"), "0.0.0.0", or "::" binds all interfaces and is NOT loopback;
// "localhost" and any address in 127.0.0.0/8 or ::1 is loopback.
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false // all interfaces
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (r *Runtime) serveGRPC(
	ctx context.Context,
	t *tomb.Tomb,
	srv *grpc.Server,
	address string,
) (net.Addr, error) {
	// ctx governs only the Listen operation (address resolution / socket
	// creation), not the returned listener's lifetime. Server shutdown is driven
	// separately via t.Dying() below, so cancelling ctx after this returns does
	// not close the listener.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", address)
	if err != nil {
		// See the comment on the equivalent DB-creation-failure branch above:
		// wrap first, then pass the wrapped error as the cause, so the
		// captured stack frame isn't discarded.
		wrapped := cerrors.Errorf("failed to listen on address %q: %w", address, err)
		// Tagged Unavailable (e.g. the address is already bound by another
		// process): pkg/conduit/exitcode maps this to exit code 3.
		return nil, conduiterr.Wrap(conduiterr.CodeUnavailable, wrapped.Error(), wrapped)
	}

	t.Go(func() error {
		return srv.Serve(ln)
	})
	t.Go(func() error {
		<-t.Dying()
		gracefullyStopped := make(chan struct{})
		go func() {
			defer close(gracefullyStopped)
			srv.GracefulStop()
		}()

		select {
		case <-gracefullyStopped:
			return nil // server stopped as expected
		case <-time.After(exitTimeout):
			return cerrors.Errorf("timeout %v exceeded while closing grpc server", exitTimeout)
		}
	})

	return ln.Addr(), nil
}

func (r *Runtime) serveHTTP(
	ctx context.Context,
	t *tomb.Tomb,
	srv *http.Server,
) (net.Addr, error) {
	// See the note in serveGRPC: ctx scopes the Listen call only, not the
	// listener's lifetime.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		// Wrap-then-pass-the-wrapped-error, same reasoning as serveGRPC above
		// (keeps the captured stack frame instead of discarding it).
		wrapped := cerrors.Errorf("failed to listen on address %q: %w", r.Config.API.HTTP.Address, err)
		// Tagged Unavailable, same reasoning as serveGRPC above.
		return nil, conduiterr.Wrap(conduiterr.CodeUnavailable, wrapped.Error(), wrapped)
	}

	t.Go(func() error {
		err := srv.Serve(ln)
		if err != nil {
			if err == http.ErrServerClosed {
				// ignore expected close
				return nil
			}
			return cerrors.Errorf("http server listening on %q stopped with error: %w", ln.Addr(), err)
		}
		return nil
	})
	t.Go(func() error {
		<-t.Dying()
		// start server shutdown with a timeout, use fresh context
		ctx, cancel := context.WithTimeout(context.Background(), exitTimeout)
		defer cancel()
		return srv.Shutdown(ctx)
	})

	return ln.Addr(), nil
}

// InitProvisioningOnly initializes just the metadata services
// (processor/connector/pipeline — the ones ProvisionService.Plan/ApplyPlan
// read and mutate) so ProvisionService is ready to use, without starting the
// gRPC/HTTP API servers, the connector-utils callback server, or any
// pipeline's actual dataflow (lifecycleService.Init, which would resume
// previously-running pipelines, is deliberately never called here).
//
// It exists for the standalone `conduit pipelines deploy|apply` CLI commands
// (see cmd/conduit/internal/deploy), which need a working ProvisionService
// without booting the full server — reusing NewRuntime's exact, real service
// construction (schema registry, builtin connector/processor registries)
// rather than a second, parallel bootstrap that could drift from it.
//
// Known limitation (documented, not silently papered over): connectorPluginService
// is initialized with an empty connector-utils address (no live callback
// server is started), so a connector plugin that needs it during OnDelete
// (invoked when apply deletes a connector, including the delete+create pair
// for a Type change) may fail to run its cleanup hook correctly in this
// standalone mode. See docs/design-documents/20260708-cli-pipeline-deploy-apply.md's
// PR failure-mode analysis.
//
// Callers must call r.DB.Close() when done; InitProvisioningOnly does not
// start anything that needs its own shutdown path (no goroutines, no
// listeners), so there is nothing else to release.
func (r *Runtime) InitProvisioningOnly(ctx context.Context) error {
	if err := r.processorService.Init(ctx); err != nil {
		return cerrors.Errorf("failed to init processor service: %w", err)
	}

	r.connectorPluginService.Init(ctx, "", r.Config.Connectors.MaxReceiveRecordSize)

	if err := r.connectorService.Init(ctx); err != nil {
		return cerrors.Errorf("failed to init connector service: %w", err)
	}

	// Invariant 7 / Tier-1 safety (see plan.go's isRunningStatus and AC-13):
	// pipelineService.Init converts any persisted StatusRunning to
	// StatusSystemStopped (it cannot know whether the process that set
	// StatusRunning is still alive) — the *same* laundering conduit run
	// itself relies on after a real restart. That means a status read
	// through *this* Runtime can never distinguish "was running before an
	// unrelated crash" from "is actually running right now in a separate,
	// live conduit run process" — only the process that actually started a
	// pipeline's goroutines (or a live RPC to it) can tell the difference.
	// See the design doc's PR failure-mode analysis for how the CLI commands
	// account for this (DB-type gate on the standalone apply path: BadgerDB/
	// SQLite's exclusive file lock makes "a live conduit run has this store
	// open too" fail at OpenStore instead of silently proceeding; Postgres,
	// which allows concurrent connections, is refused outright).
	if err := r.pipelineService.Init(ctx); err != nil {
		return cerrors.Errorf("failed to init pipeline service: %w", err)
	}

	return nil
}

func (r *Runtime) initServices(ctx context.Context, t *tomb.Tomb) error {
	err := r.processorService.Init(ctx)
	if err != nil {
		return cerrors.Errorf("failed to init processor service: %w", err)
	}

	// Initialize APIs needed by connector plugins
	// Needs to be initialized before connectorPluginService
	// because the standalone connector registry needs to run all plugins,
	// and the plugins initialize a connector utils client when they are run.
	connUtilsAddr, err := r.startConnectorUtils(ctx, t)
	if err != nil {
		return cerrors.Errorf("failed to start connector utilities API: %w", err)
	}
	r.logger.Info(ctx).Msgf("connector utilities started on %v", connUtilsAddr)

	r.connectorPluginService.Init(ctx, connUtilsAddr.String(), r.Config.Connectors.MaxReceiveRecordSize)

	err = r.connectorService.Init(ctx)
	if err != nil {
		return cerrors.Errorf("failed to init connector service: %w", err)
	}

	if r.Config.Pipelines.ExitOnDegraded {
		if r.Config.Preview.PipelineArchV2 {
			ls := r.lifecycleService.(*lifecycle_v2.Service)
			ls.OnFailure(func(e lifecycle_v2.FailureEvent) {
				r.logger.Warn(ctx).
					Err(e.Error).
					Str(log.PipelineIDField, e.ID).
					Msg("Conduit will shut down due to a pipeline failure and 'exit-on-degraded' enabled")
				t.Kill(cerrors.Errorf("shut down due to 'exit-on-degraded' error: %w", e.Error))
			})
		} else {
			ls := r.lifecycleService.(*lifecycle.Service)
			ls.OnFailure(func(e lifecycle.FailureEvent) {
				r.logger.Warn(ctx).
					Err(e.Error).
					Str(log.PipelineIDField, e.ID).
					Msg("Conduit will shut down due to a pipeline failure and 'exit-on-degraded' enabled")
				t.Kill(cerrors.Errorf("shut down due to 'exit-on-degraded' error: %w", e.Error))
			})
		}
	}
	err = r.pipelineService.Init(ctx)
	if err != nil {
		return cerrors.Errorf("failed to init pipeline service: %w", err)
	}

	err = r.ProvisionService.Init(ctx)
	if err != nil {
		cerrors.ForEach(err, func(err error) {
			r.logger.Err(ctx, err).Msg("provisioning failed")
		})
		if r.Config.Pipelines.ExitOnDegraded {
			r.logger.Warn(ctx).
				Err(err).
				Msg("Conduit will shut down due to a pipeline provisioning failure and 'exit on error' enabled")
			err = cerrors.Errorf("shut down due to 'exit on error' enabled: %w", err)
			return err
		}
	}

	err = r.lifecycleService.Init(ctx)
	if err != nil {
		cerrors.ForEach(err, func(err error) {
			r.logger.Err(ctx, err).Msg("pipeline failed to be started")
		})
	}

	if r.Config.Dev.Enabled {
		if err := r.startDevWatcher(ctx, t); err != nil {
			return cerrors.Errorf("failed to start dev watcher: %w", err)
		}
	}

	return nil
}

// startDevWatcher starts the `conduit run --dev` hot-reload file watcher
// (pkg/conduit/dev) as a tomb-managed goroutine, once startup provisioning
// (ProvisionService.Init above) and pipeline auto-resume
// (lifecycleService.Init above) have both already run — the watcher only
// ever reacts to *subsequent* edits, per
// docs/design-documents/20260712-pipeline-dev-hot-reload.md §4.
//
// Invariant 7: ctx here is the tomb-derived context Run constructed at its
// top (`t, ctx := tomb.WithContext(ctx)`), so Ctrl-C/SIGTERM cancelling it
// cancels the watcher the same way it cancels every other service — t.Go
// makes dev.Watcher.Run's return value part of the tomb's shutdown
// accounting, and a normal cancellation (context.Canceled) is translated to
// nil so it is never mistaken for a watcher failure.
func (r *Runtime) startDevWatcher(ctx context.Context, t *tomb.Tomb) error {
	w, err := dev.New(r.ProvisionService, r.lifecycleService, r.devPipelineStatus, dev.Options{
		Path:   r.Config.Pipelines.Path,
		Logger: r.logger,
		Out:    os.Stdout,
		JSON:   r.Config.Dev.JSON,
	})
	if err != nil {
		return err
	}

	t.Go(func() error {
		err := w.Run(ctx)
		if err != nil && cerrors.Is(err, context.Canceled) {
			return nil
		}
		return err
	})
	return nil
}

// devPipelineStatus is the dev.StatusFunc the watcher uses purely to label
// an apply accurately (see dev.StatusFunc's doc) — it reports whether
// pipelineID currently has live, in-process work, mirroring
// provisioning.isRunningStatus's classification (pkg/provisioning/plan.go),
// which is unexported and cannot be called from here. This duplicates a
// three-case predicate, not engine behavior: keep it in sync with
// provisioning's own definition if that classification ever changes.
func (r *Runtime) devPipelineStatus(ctx context.Context, pipelineID string) (bool, error) {
	inst, err := r.Orchestrator.Pipelines.Get(ctx, pipelineID)
	if err != nil {
		if cerrors.Is(err, pipeline.ErrInstanceNotFound) {
			return false, nil
		}
		return false, err
	}
	switch inst.GetStatus() {
	case pipeline.StatusRunning, pipeline.StatusRecovering, pipeline.StatusDegraded:
		return true, nil
	case pipeline.StatusSystemStopped, pipeline.StatusUserStopped:
		return false, nil
	default:
		return false, nil
	}
}
