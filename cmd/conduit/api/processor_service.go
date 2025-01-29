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
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
)

//go:generate mockgen -destination=mock/processor_service.go -package=mock . ProcessorService

// ProcessorService defines the methods of the ProcessorServiceClient that are currently used by the CLI.
type ProcessorService interface {
	ListProcessors(ctx context.Context, in *apiv1.ListProcessorsRequest, opts ...grpc.CallOption) (*apiv1.ListProcessorsResponse, error)
	GetProcessor(ctx context.Context, in *apiv1.GetProcessorRequest, opts ...grpc.CallOption) (*apiv1.GetProcessorResponse, error)
	ListProcessorPlugins(ctx context.Context, in *apiv1.ListProcessorPluginsRequest, opts ...grpc.CallOption) (*apiv1.ListProcessorPluginsResponse, error)
}
