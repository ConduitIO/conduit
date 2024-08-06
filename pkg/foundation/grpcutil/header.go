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

package grpcutil

import (
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

const (
	// HTTPEndpointHeader is added internally by the GRPC Gateway to signal the
	// GRPC handler the request was actually received by an HTTP endpoint.
	HTTPEndpointHeader = "x-http-endpoint"

	// RequestIDHeader contains a value that uniquely identifies a request.
	RequestIDHeader = "x-request-id"
)

func HeaderMatcher(key string) (string, bool) {
	switch strings.ToLower(key) {
	case RequestIDHeader, HTTPEndpointHeader:
		return key, true
	default:
		return runtime.DefaultHeaderMatcher(key)
	}
}
