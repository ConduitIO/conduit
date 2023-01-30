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
	"github.com/conduitio/conduit/pkg/foundation/database"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	grpcstatus "google.golang.org/grpc/status"
)

// HealthChecker implements the gRPC health service.
// For more information, see: https://github.com/grpc/grpc/blob/master/doc/health-checking.md
type HealthChecker struct {
	grpc_health_v1.UnimplementedHealthServer
	db database.DB
}

func (s *HealthChecker) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	var status grpc_health_v1.HealthCheckResponse_ServingStatus
	switch req.Service {
	case "", "PipelineService", "ConnectorService", "ProcessorService":
		// PipelineService, ConnectorService, ProcessorService require a DB to work
		if err := s.db.Ping(ctx); err != nil {
			status = grpc_health_v1.HealthCheckResponse_NOT_SERVING
		} else {
			status = grpc_health_v1.HealthCheckResponse_SERVING
		}
	case "InformationService", "PluginService":
		// InformationService doesn't require anything to work
		// PluginService requires the file system only
		status = grpc_health_v1.HealthCheckResponse_SERVING
	default:
		return nil, grpcstatus.Errorf(codes.NotFound, "service %q not found", req.Service)
	}

	return &grpc_health_v1.HealthCheckResponse{Status: status}, nil
}

func (s *HealthChecker) Watch(req *grpc_health_v1.HealthCheckRequest, server grpc_health_v1.Health_WatchServer) error {
	// should be altered to subsequently send a new message whenever the service's serving status changes.
	return server.Send(&grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	})
}

func (s *HealthChecker) checkDB(ctx context.Context) error {
	return nil
}

func NewHealthChecker(db database.DB) *HealthChecker {
	return &HealthChecker{db: db}
}
