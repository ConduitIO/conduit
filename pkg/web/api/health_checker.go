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
	"github.com/conduitio/conduit/pkg/foundation/multierror"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	grpcstatus "google.golang.org/grpc/status"
)

type Checker interface {
	Check(ctx context.Context) error
}

// HealthChecker implements the gRPC health service.
// For more information, see: https://github.com/grpc/grpc/blob/master/doc/health-checking.md
type HealthChecker struct {
	grpc_health_v1.UnimplementedHealthServer
	checkers map[string]Checker
}

func (c *HealthChecker) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	// todo log error

	// Check all services
	if req.Service == "" {
		if err := c.checkAll(ctx); err != nil {
			return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING}, nil
		}
		return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
	}

	if _, ok := c.checkers[req.Service]; ok {
		if err := c.checkers[req.Service].Check(ctx); err != nil {
			return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING}, nil
		}
		return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
	}

	return nil, grpcstatus.Errorf(codes.NotFound, "service '%v' not found", req.Service)
}

func (c *HealthChecker) Watch(req *grpc_health_v1.HealthCheckRequest, server grpc_health_v1.Health_WatchServer) error {
	// should be altered to subsequently send a new message whenever the service's serving status changes.
	return server.Send(&grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	})
}

func (c *HealthChecker) checkAll(ctx context.Context) error {
	var merr error
	for _, checker := range c.checkers {
		merr = multierror.Append(merr, checker.Check(ctx))
	}
	return merr
}

func NewHealthChecker(checkers map[string]Checker) *HealthChecker {
	return &HealthChecker{checkers: checkers}
}
