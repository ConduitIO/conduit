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

package log

import (
	"context"

	"github.com/rs/zerolog"
)

// componentHook adds the component name to the log output.
type componentHook struct {
	name string
}

// Run executes the componentHook.
func (ch componentHook) Run(_ context.Context, e *zerolog.Event, _ zerolog.Level) {
	if ch.name != "" {
		e.Str(ComponentField, ch.name)
	}
}
