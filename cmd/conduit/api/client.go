// Copyright © 2025 Meroxa, Inc.
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
	"fmt"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
)

type Client struct {
	conn            *grpc.ClientConn
	pipelineService apiv1.PipelineServiceClient
	HealthService   healthgrpc.HealthClient
}

func NewClient(_ context.Context, address string) (*Client, error) {
	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client: %w", err)
	}

	return &Client{
		conn:            conn,
		pipelineService: apiv1.NewPipelineServiceClient(conn),
		HealthService:   healthgrpc.NewHealthClient(conn),
	}, nil
}

func (c *Client) ListPipelines(ctx context.Context) (*apiv1.ListPipelinesResponse, error) {
	return c.pipelineService.ListPipelines(ctx, &apiv1.ListPipelinesRequest{})
}

func (c *Client) Close() error {
	return c.conn.Close()
}
