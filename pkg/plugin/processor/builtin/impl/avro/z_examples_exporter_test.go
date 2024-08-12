// Copyright © 2024 Meroxa, Inc.
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

// This file is here to export processor specifications. It's named with a "z_"
// prefix to ensure it's processed last by go generate.

//go:generate env EXPORT_PROCESSORS=true go test -count=1 -tags export_processors .

package avro

import (
	"os"
	"testing"

	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal/exampleutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if code > 0 {
		os.Exit(code)
	}

	if os.Getenv("EXPORT_PROCESSORS") == "true" {
		// tests passed and env var is included, export the processors
		exampleutil.ExportProcessors()
	}
}
