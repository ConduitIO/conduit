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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
)

type Client struct {
	conn                   *grpc.ClientConn
	PipelineServiceClient  PipelineService
	ConnectorServiceClient ConnectorService
	ProcessorServiceClient ProcessorService
	HealthClient           healthgrpc.HealthClient
}

func NewClient(ctx context.Context, address string) (*Client, error) {
	conn, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, cerrors.Errorf("failed to create gRPC client: %w", err)
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
		// cerrors.Errorf always captures a stack frame (unlike fmt.Errorf),
		// regardless of whether %w or %v is used; %v is deliberate here (err
		// may be nil, when the response came back with a non-SERVING status
		// and no transport error) so this must not use %w with a possibly-nil
		// operand. Passing the wrapped error itself (not the raw err) as
		// Wrap's cause keeps that frame: conduiterr.Wrap only synthesizes a
		// frame of its own when cause is nil, so handing it a frame-less raw
		// err here would silently produce a traceless ConduitError.
		wrapped := cerrors.Errorf("we couldn't connect to Conduit at the configured address %q\n"+
			"Please execute `conduit run` to start it.\nTo check the current configured `api.grpc.address`, run `conduit config`\n\n"+
			"Error details: %v", address, err)
		// Tagged Unavailable (an environment failure, not a validation or
		// internal one): pkg/conduit/exitcode maps it to exit code 3 so a
		// script or agent can tell "the server isn't reachable" apart from a
		// rejected request or a bug.
		return conduiterr.Wrap(conduiterr.CodeUnavailable, wrapped.Error(), wrapped)
	}
	return nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}
