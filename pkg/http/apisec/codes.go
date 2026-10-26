// Copyright © 2026 Meroxa, Inc.
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

package apisec

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/grpc/codes"
)

// CodeTokenRequired is raised at startup when the HTTP or gRPC API is bound
// to a non-loopback address and no API token is configured. Conduit refuses to
// serve an unauthenticated API to the network.
var CodeTokenRequired = conduiterr.Register("api.token_required", codes.FailedPrecondition)

// CodeTokenInvalid is raised at startup when the configured API token is
// unusable: the token file cannot be read, the token is empty or too short,
// or both api.token and api.token-file are set.
var CodeTokenInvalid = conduiterr.Register("api.token_invalid", codes.InvalidArgument)

// CodeUnauthenticated is returned by the HTTP and gRPC APIs when a request
// carries no bearer token or the wrong one.
var CodeUnauthenticated = conduiterr.Register("api.unauthenticated", codes.Unauthenticated)

// CodeOriginForbidden is returned by the HTTP API when a request's Origin
// header is neither allowlisted nor same-origin.
var CodeOriginForbidden = conduiterr.Register("api.origin_forbidden", codes.PermissionDenied)

// CodeHostForbidden is returned by the HTTP API on a loopback bind when the
// Host header is not localhost, 127.0.0.1, [::1] or a configured allowed
// host (DNS rebinding protection).
var CodeHostForbidden = conduiterr.Register("api.host_forbidden", codes.PermissionDenied)

// CodeContentTypeUnsupported is returned by the HTTP API when a mutating
// request (POST, PUT, PATCH, DELETE) with a body does not use
// Content-Type: application/json.
var CodeContentTypeUnsupported = conduiterr.Register("api.content_type_unsupported", codes.InvalidArgument)
