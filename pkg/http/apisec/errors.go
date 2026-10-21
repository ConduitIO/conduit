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
	"net/http"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	// ConfigPathAllowedOrigins and ConfigPathAllowedHosts are the config
	// paths reported in browser-guard refusals.
	ConfigPathAllowedOrigins = "api.http.cors.allowed-origins"
	ConfigPathAllowedHosts   = "api.http.allowed-hosts"
)

// writeHTTPError writes e in the same JSON shape the gRPC gateway uses for
// errors (google.rpc.Status with an ErrorInfo detail).
func writeHTTPError(w http.ResponseWriter, httpStatus int, e *conduiterr.ConduitError) {
	b, err := protojson.Marshal(conduiterr.ToStatus(e).Proto())
	if err != nil {
		b = []byte(`{"message":"request refused"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(httpStatus)
	_, _ = w.Write(b)
}
