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

//go:build !ui

package ui

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

//go:embed distub/*
var distubFS embed.FS

func newUIAssetFS() (http.FileSystem, error) {
	return assetFS(distubFS, "distub")
}

func assetFS(embeddedFS embed.FS, dir string) (http.FileSystem, error) {
	subFS, err := fs.Sub(embeddedFS, dir)
	if err != nil {
		panic(cerrors.Errorf("couldn't create sub filesystem: %w", err))
	}

	return &SPARoutingFS{FileSystem: http.FS(subFS)}, nil
}
