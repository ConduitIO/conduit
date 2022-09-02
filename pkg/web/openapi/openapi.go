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

//go:generate ./copy-swagger-api.sh

package openapi

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

//go:embed swagger-ui/*
var swaggerUI embed.FS

// Handler serves an OpenAPI UI.
func Handler() http.Handler {
	subFS, err := fs.Sub(swaggerUI, "swagger-ui")
	if err != nil {
		panic(cerrors.Errorf("couldn't create sub filesystem: %w", err))
	}
	return http.FileServer(http.FS(subFS))
}
