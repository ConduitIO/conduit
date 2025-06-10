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
	"os"
	"runtime"
	"runtime/pprof"
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
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
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
}

// lifecycleService is an interface that we use temporarily to allow for
// both the old and new lifecycle services to be used interchangeably.
type lifecycleService interface {
	Start(ctx context.Context, pipelineID string) error
	Stop(ctx context.Context, pipelineID string, force bool) error
	Init(ctx context.Context) error
}

// NewRuntime sets up a Runtime instance and primes it for start.
func NewRuntime(cfg Config) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, cerrors.Errorf("invalid config: %w", err)
	}

	logger := cfg.Log.NewLogger(cfg.Log.Level, cfg.Log.Format)
	logger.Logger = logger.
		Hook(ctxutil.MessageIDLogCtxHook{}).
		Hook(ctxutil.RequestIDLogCtxHook{}).
		Hook(ctxutil.FilepathLogCtxHook{})
	zerolog.DefaultContextLogger = &logger.Logger

	var db database.DB
	db = cfg.DB.Driver

	if db == nil {
		var err error
		switch cfg.DB.Type {
		case DBTypeBadger:
			db, err = badger.New(logger.Logger, cfg.DB.Badger.Path)
		case DBTypePostgres:
			db, err = postgres.New(context.Background(), logger.Logger, cfg.DB.Postgres.ConnectionString, cfg.DB.Postgres.Table)
		case DBTypeInMemory:
			db = &inmemory.DB{}
			logger.Warn(context.Background()).Msg("Using in-memory store, all pipeline configurations will be lost when Conduit stops.")
		case DBTypeSQLite:
			db, err = sqlite.New(context.Background(), logger.Logger, cfg.DB.SQLite.Path, cfg.DB.SQLite.Table)
		default:
			err = cerrors.Errorf("invalid DB type %q", cfg.DB.Type)
		}
		if err != nil {
			return nil, cerrors.Errorf("failed to create a DB instance: %w", err)
		}
	}

	configureMetrics()
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

		logger: logger,
	}

	err := createServices(r)
	if err != nil {
		return nil, cerrors.Errorf("failed to initialize services: %w", err)
	}

	return r, nil
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

// Run initializes all of Conduit's underlying services and starts the GRPC and
// HTTP APIs. This function blocks until the supplied context is cancelled or
// one of the services experiences a fatal error.
func (r *Runtime) Run(ctx context.Context) (err error) {
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
			return r.DB.Close()
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
			return r.DB.Close()
		})

		return nil
	})
}

func (r *Runtime) newHTTPMetricsHandler() http.Handler {
	return promhttp.Handler()
}

func (r *Runtime) serveGRPCAPI(ctx context.Context, t *tomb.Tomb) (net.Addr, error) {
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpcutil.RequestIDUnaryServerInterceptor(r.logger),
			grpcutil.LoggerUnaryServerInterceptor(r.logger),
		),
		grpc.StatsHandler(metricsGrpcStatsHandler),
		grpc.MaxRecvMsgSize(10*1024*1024),
	)

	pipelineAPIv1 := api.NewPipelineAPIv1(r.Orchestrator.Pipelines)
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
		grpc.StatsHandler(metricsGrpcStatsHandler),
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

	handler := grpcutil.WithWebsockets(
		ctx,
		grpcutil.WithDefaultGatewayMiddleware(allowCORS(gwmux, "http://localhost:4200")),
		r.logger,
	)

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

func preflightHandler(w http.ResponseWriter) {
	headers := []string{"Content-Type", "Accept"}
	w.Header().Set("Access-Control-Allow-Headers", strings.Join(headers, ","))
	methods := []string{"GET", "HEAD", "POST", "PUT", "DELETE"}
	w.Header().Set("Access-Control-Allow-Methods", strings.Join(methods, ","))
}

// allowCORS allows Cross Origin Resource Sharing from any origin.
// Don't do this without consideration in production systems.
func allowCORS(h http.Handler, origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			if r.Method == "OPTIONS" && r.Header.Get("Access-Control-Request-Method") != "" {
				preflightHandler(w)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func (r *Runtime) serveGRPC(
	ctx context.Context,
	t *tomb.Tomb,
	srv *grpc.Server,
	address string,
) (net.Addr, error) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, cerrors.Errorf("failed to listen on address %q: %w", address, err)
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
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return nil, cerrors.Errorf("failed to listen on address %q: %w", r.Config.API.HTTP.Address, err)
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

	return nil
}
