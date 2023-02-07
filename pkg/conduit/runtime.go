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
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/ctxutil"
	"github.com/conduitio/conduit/pkg/foundation/database"
	"github.com/conduitio/conduit/pkg/foundation/database/badger"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/database/postgres"
	"github.com/conduitio/conduit/pkg/foundation/grpcutil"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
	"github.com/conduitio/conduit/pkg/foundation/metrics/prometheus"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/orchestrator"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/builtin"
	"github.com/conduitio/conduit/pkg/plugin/standalone"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/provisioning"
	"github.com/conduitio/conduit/pkg/web/api"
	"github.com/conduitio/conduit/pkg/web/openapi"
	"github.com/conduitio/conduit/pkg/web/ui"
	apiv1 "github.com/conduitio/conduit/proto/gen/api/v1"
	grpcruntime "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/piotrkowalczuk/promgrpc/v4"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/stats"
	"gopkg.in/tomb.v2"

	// NB: anonymous import triggers processor registry creation
	_ "github.com/conduitio/conduit/pkg/processor/procbuiltin"
	_ "github.com/conduitio/conduit/pkg/processor/procjs"
)

const (
	exitTimeout = 10 * time.Second
)

// Runtime sets up all services for serving and monitoring a Conduit instance.
type Runtime struct {
	Config Config

	DB           database.DB
	Orchestrator *orchestrator.Orchestrator

	pipelineService  *pipeline.Service
	connectorService *connector.Service
	processorService *processor.Service
	pluginService    *plugin.Service
	provisionService *provisioning.Service

	connectorPersister *connector.Persister

	logger log.CtxLogger
}

// NewRuntime sets up a Runtime instance and primes it for start.
func NewRuntime(cfg Config) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, cerrors.Errorf("invalid config: %w", err)
	}

	logger := newLogger(cfg.Log.Level, cfg.Log.Format)

	var db database.DB
	var err error
	switch cfg.DB.Type {
	case DBTypeBadger:
		db, err = badger.New(logger.Logger, cfg.DB.Badger.Path)
	case DBTypePostgres:
		db, err = postgres.New(context.Background(), logger, cfg.DB.Postgres.ConnectionString, cfg.DB.Postgres.Table)
	case DBTypeInMemory:
		db = &inmemory.DB{}
		logger.Warn(context.Background()).Msg("Using in-memory store, all pipeline configurations will be lost when Conduit stops.")
	default:
		err = cerrors.Errorf("invalid DB type %q", cfg.DB.Type)
	}
	if err != nil {
		return nil, cerrors.Errorf("failed to create a DB instance: %w", err)
	}

	configurePrometheus()
	measure.ConduitInfo.WithValues(Version(true)).Inc()

	// Start the connector persister
	connectorPersister := connector.NewPersister(logger, db,
		connector.DefaultPersisterDelayThreshold,
		connector.DefaultPersisterBundleCountThreshold,
	)

	// Create all necessary internal services
	plService, connService, procService, pluginService, err := newServices(logger, db, connectorPersister, cfg.Connectors.Path)
	if err != nil {
		return nil, cerrors.Errorf("failed to create services: %w", err)
	}

	provisionService := provisioning.NewService(db, logger, plService, connService, procService, pluginService, cfg.Pipelines.Path)

	orc := orchestrator.NewOrchestrator(db, logger, plService, connService, procService, pluginService)

	r := &Runtime{
		Config:       cfg,
		DB:           db,
		Orchestrator: orc,

		pipelineService:  plService,
		connectorService: connService,
		processorService: procService,
		pluginService:    pluginService,
		provisionService: provisionService,

		connectorPersister: connectorPersister,

		logger: logger,
	}
	return r, nil
}

func newLogger(level string, format string) log.CtxLogger {
	// TODO make logger hooks configurable
	l, _ := zerolog.ParseLevel(level)
	f, _ := log.ParseFormat(format)
	logger := log.InitLogger(l, f)
	logger = logger.CtxHook(
		ctxutil.MessageIDLogCtxHook{},
		ctxutil.RequestIDLogCtxHook{},
	)
	return logger
}

func configurePrometheus() {
	registry := prometheus.NewRegistry(nil)
	promclient.MustRegister(registry)
	metrics.Register(registry)
}

func newServices(
	logger log.CtxLogger,
	db database.DB,
	connPersister *connector.Persister,
	pluginsPath string,
) (*pipeline.Service, *connector.Service, *processor.Service, *plugin.Service, error) {
	pipelineService := pipeline.NewService(logger, db)
	connectorService := connector.NewService(logger, db, connPersister)
	processorService := processor.NewService(logger, db, processor.GlobalBuilderRegistry)
	pluginService := plugin.NewService(
		logger,
		builtin.NewRegistry(logger, builtin.DefaultDispenserFactories),
		standalone.NewRegistry(logger, pluginsPath),
	)

	return pipelineService, connectorService, processorService, pluginService, nil
}

// Run initializes all of Conduit's underlying services and starts the GRPC and
// HTTP APIs. This function blocks until the supplied context is cancelled or
// one of the services experiences a fatal error.
func (r *Runtime) Run(ctx context.Context) (err error) {
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

	// Init each service
	err = r.processorService.Init(ctx)
	if err != nil {
		return cerrors.Errorf("failed to init processor service: %w", err)
	}
	err = r.connectorService.Init(ctx)
	if err != nil {
		return cerrors.Errorf("failed to init connector service: %w", err)
	}
	err = r.pipelineService.Init(ctx)
	if err != nil {
		return cerrors.Errorf("failed to init pipeline service: %w", err)
	}

	err = r.provisionService.Init(ctx)
	if err != nil {
		multierror.ForEach(err, func(err error) {
			r.logger.Err(ctx, err).Msg("provisioning failed")
		})
	}

	err = r.pipelineService.Run(ctx, r.connectorService, r.processorService, r.pluginService)
	if err != nil {
		multierror.ForEach(err, func(err error) {
			r.logger.Err(ctx, err).Msg("pipeline failed to be started")
		})
	}

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
	r.logger.Info(ctx).Msgf("click here to navigate to Conduit UI: http://localhost:%d/ui", port)
	r.logger.Info(ctx).Msgf("click here to navigate to explore the HTTP API: http://localhost:%d/openapi", port)
	r.logger.Info(ctx).Send()

	return nil
}

func (r *Runtime) registerCleanup(t *tomb.Tomb) {
	t.Go(func() error {
		<-t.Dying()
		// start cleanup with a fresh context
		ctx := context.Background()

		// t.Err() can be nil, when we had a call: t.Kill(nil)
		// t.Err() will be context.Canceled, if the tomb's context was canceled
		if t.Err() == nil || cerrors.Is(t.Err(), context.Canceled) {
			r.pipelineService.StopAll(ctx, pipeline.ErrGracefulShutdown)
		} else {
			// tomb died due to a real error
			r.pipelineService.StopAll(ctx, cerrors.Errorf("conduit experienced an error: %w", t.Err()))
		}
		err := r.pipelineService.Wait(exitTimeout)
		t.Go(func() error {
			r.connectorPersister.Wait()
			return r.DB.Close()
		})
		return err
	})
}

func (r *Runtime) newGrpcStatsHandler() stats.Handler {
	// We are manually creating the stats handler and not using
	// promgrpc.ServerStatsHandler(), because we don't need metrics related to
	// messages. They would be relevant for GRPC streams, we don't use them.
	grpcStatsHandler := promgrpc.NewStatsHandler(
		promgrpc.NewServerConnectionsStatsHandler(promgrpc.NewServerConnectionsGaugeVec()),
		promgrpc.NewServerRequestsTotalStatsHandler(promgrpc.NewServerRequestsTotalCounterVec()),
		promgrpc.NewServerRequestsInFlightStatsHandler(promgrpc.NewServerRequestsInFlightGaugeVec()),
		promgrpc.NewServerRequestDurationStatsHandler(promgrpc.NewServerRequestDurationHistogramVec()),
		promgrpc.NewServerResponsesTotalStatsHandler(promgrpc.NewServerResponsesTotalCounterVec()),
	)
	promclient.MustRegister(grpcStatsHandler)
	return grpcStatsHandler
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
		grpc.StatsHandler(r.newGrpcStatsHandler()),
	)

	pipelineAPIv1 := api.NewPipelineAPIv1(r.Orchestrator.Pipelines)
	pipelineAPIv1.Register(grpcServer)

	processorAPIv1 := api.NewProcessorAPIv1(r.Orchestrator.Processors)
	processorAPIv1.Register(grpcServer)

	connectorAPIv1 := api.NewConnectorAPIv1(r.Orchestrator.Connectors)
	connectorAPIv1.Register(grpcServer)

	pluginAPIv1 := api.NewPluginAPIv1(r.Orchestrator.Plugins)
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
			"PipelineService":  r.pipelineService,
			"ConnectorService": r.connectorService,
			"ProcessorService": r.processorService,
			"PluginService":    r.pluginService,
		},
		r.logger,
	)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)

	// serve grpc server
	return r.serveGRPC(ctx, t, grpcServer)
}

func (r *Runtime) serveHTTPAPI(
	ctx context.Context,
	t *tomb.Tomb,
	addr net.Addr,
) (net.Addr, error) {
	conn, err := grpc.DialContext(ctx, addr.String(),
		grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
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

	uiHandler, err := ui.Handler()
	if err != nil {
		return nil, cerrors.Errorf("failed to set up ui handler: %w", err)
	}

	uiHandler = http.StripPrefix("/ui", uiHandler)

	err = gwmux.HandlePath(
		"GET",
		"/ui/**",
		func(w http.ResponseWriter, req *http.Request, pathParams map[string]string) {
			uiHandler.ServeHTTP(w, req)
		},
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to register ui handler: %w", err)
	}

	err = gwmux.HandlePath(
		"GET",
		"/",
		func(w http.ResponseWriter, req *http.Request, pathParams map[string]string) {
			http.Redirect(w, req, "/ui", http.StatusFound)
		},
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to register redirect handler: %w", err)
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
		grpcutil.WithDefaultGatewayMiddleware(
			r.logger, allowCORS(gwmux, "http://localhost:4200"),
		),
		r.logger,
	)

	return r.serveHTTP(
		ctx,
		t,
		&http.Server{
			Addr:              r.Config.HTTP.Address,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		},
	)
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
) (net.Addr, error) {
	ln, err := net.Listen("tcp", r.Config.GRPC.Address)
	if err != nil {
		return nil, cerrors.Errorf("failed to listen on address %q: %w", r.Config.GRPC.Address, err)
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

	r.logger.Info(ctx).Str(log.ServerAddressField, ln.Addr().String()).Msg("grpc server started")
	return ln.Addr(), nil
}

func (r *Runtime) serveHTTP(
	ctx context.Context,
	t *tomb.Tomb,
	srv *http.Server,
) (net.Addr, error) {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return nil, cerrors.Errorf("failed to listen on address %q: %w", r.Config.GRPC.Address, err)
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

	r.logger.Info(ctx).Str(log.ServerAddressField, ln.Addr().String()).Msg("http server started")
	return ln.Addr(), nil
}
