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

package api

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	grpcstatus "google.golang.org/grpc/status"
)

type Checker interface {
	Check(ctx context.Context) error
}

// HealthServer implements the gRPC health service.
// Using the HealthServer, it's possible to check
// the gRPC server's overall health, but also the
// health of the individual gRPC services.
// For more information, see: https://github.com/grpc/grpc/blob/master/doc/health-checking.md
type HealthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	checkers map[string]Checker
	logger   log.CtxLogger
}

func NewHealthServer(checkers map[string]Checker, log log.CtxLogger) *HealthServer {
	return &HealthServer{
		checkers: checkers,
		logger:   log.WithComponent("api.HealthServer"),
	}
}

func (h *HealthServer) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	// Check all services
	if req.Service == "" {
		if h.checkAll(ctx) {
			return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
		}
		return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING}, nil
	}

	if _, ok := h.checkers[req.Service]; ok {
		if h.check(ctx, req.Service) {
			return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
		}
		return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING}, nil
	}

	return nil, grpcstatus.Errorf(codes.NotFound, "service '%v' not found", req.Service)
}

func (h *HealthServer) Watch(_ *grpc_health_v1.HealthCheckRequest, server grpc_health_v1.Health_WatchServer) error {
	// should be altered to subsequently send a new message whenever the service's serving status changes.
	return server.Send(&grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	})
}

// checkAll checks returns `true` if all service are healthy,
// returns `false` otherwise (one or more services are not healthy).
func (h *HealthServer) checkAll(ctx context.Context) bool {
	ok := true
	for service := range h.checkers {
		// we don't want to stop as soon as we find an unhealthy service
		// so that we can log issues in all unhealthy services
		// (which could help investigate an issue, correlate problems etc.)
		ok = ok && h.check(ctx, service)
	}
	return ok
}

func (h *HealthServer) check(ctx context.Context, service string) bool {
	err := h.checkers[service].Check(ctx)
	if err != nil {
		h.logger.Err(ctx, err).
			Str("service_name", service).
			Msg("health check not OK")
	}
	return err == nil
}
