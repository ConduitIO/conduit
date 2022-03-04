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

package file

import (
	"github.com/conduitio/conduit/pkg/plugin/sdk"
)

func Specification() sdk.Specification {
	return sdk.Specification{
		Name:    "file",
		Summary: "A file source and destination plugin for Conduit, written in Go.",
		Version: "v0.0.1",
		Author:  "Meroxa, Inc.",
		DestinationParams: map[string]sdk.Parameter{
			"path": {
				Default:     "",
				Description: "the file path where the file destination writes messages",
				Required:    true,
			},
		},
		SourceParams: map[string]sdk.Parameter{
			"path": {
				Default:     "",
				Description: "the file path from which the file source reads messages",
				Required:    true,
			},
		},
	}
}
