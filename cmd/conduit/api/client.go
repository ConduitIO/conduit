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
	conn *grpc.ClientConn
	apiv1.PipelineServiceClient
	apiv1.ProcessorServiceClient
	apiv1.ConnectorServiceClient
	healthgrpc.HealthClient
}

func NewClient(ctx context.Context, address string) (*Client, error) {
	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client: %w", err)
	}

	client := &Client{
		conn:                   conn,
		PipelineServiceClient:  apiv1.NewPipelineServiceClient(conn),
		ProcessorServiceClient: apiv1.NewProcessorServiceClient(conn),
		ConnectorServiceClient: apiv1.NewConnectorServiceClient(conn),
		HealthClient:           healthgrpc.NewHealthClient(conn),
	}

	if err := client.CheckHealth(ctx, address); err != nil {
		client.Close()
		return nil, err
	}

	return client, nil
}

func (c *Client) CheckHealth(ctx context.Context, address string) error {
	healthResp, err := c.HealthClient.Check(ctx, &healthgrpc.HealthCheckRequest{})
	if err != nil || healthResp.Status != healthgrpc.HealthCheckResponse_SERVING {
		return fmt.Errorf("we couldn't connect to Conduit at the configured address %q\n"+
			"Please execute `conduit run` to start it.\nTo check the current configured `api.grpc.address`, run `conduit config`\n\n"+
			"Error details: %v", address, err)
	}
	return nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}
