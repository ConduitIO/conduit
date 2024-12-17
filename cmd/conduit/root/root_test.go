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

package root

import (
	"testing"

	"github.com/conduitio/ecdysis"
	isT "github.com/matryer/is"
)

func TestRootCommandFlags(t *testing.T) {
	is := isT.New(t)

	expectedFlags := []struct {
		longName   string
		shortName  string
		required   bool
		persistent bool
		hidden     bool
	}{
		{longName: "config.path", persistent: true},
		{longName: "version", shortName: "v", persistent: true},
		{longName: "db.type"},
		{longName: "db.badger.path"},
		{longName: "db.postgres.connection-string"},
		{longName: "db.postgres.table"},
		{longName: "db.sqlite.path"},
		{longName: "db.sqlite.table"},
		{longName: "api.enabled"},
		{longName: "http.address"},
		{longName: "grpc.address"},
		{longName: "log.level"},
		{longName: "log.format"},
		{longName: "connectors.path"},
		{longName: "processors.path"},
		{longName: "pipelines.path"},
		{longName: "pipelines.exit-on-degraded"},
		{longName: "pipelines.error-recovery.min-delay"},
		{longName: "pipelines.error-recovery.max-delay"},
		{longName: "pipelines.error-recovery.backoff-factor"},
		{longName: "pipelines.error-recovery.max-retries"},
		{longName: "pipelines.error-recovery.max-retries-window"},
		{longName: "schema-registry.type"},
		{longName: "schema-registry.confluent.connection-string"},
		{longName: "preview.pipeline-arch-v2"},
		{longName: "dev.cpuprofile"},
		{longName: "dev.memprofile"},
		{longName: "dev.blockprofile"},
	}

	c := &RootCommand{}
	flags := c.Flags()

	for _, ef := range expectedFlags {
		var foundFlag *ecdysis.Flag
		for _, f := range flags {
			if f.Long == ef.longName {
				foundFlag = &f
				break
			}
		}

		is.True(foundFlag != nil)

		if foundFlag != nil {
			is.Equal(ef.shortName, foundFlag.Short)
			is.Equal(ef.required, foundFlag.Required)
			is.Equal(ef.persistent, foundFlag.Persistent)
			is.Equal(ef.hidden, foundFlag.Hidden)
		}
	}
}
