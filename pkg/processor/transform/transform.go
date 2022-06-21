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

package transform

import (
	"context"

	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

// Transform takes a record and if successful returns the transformed record,
// an error otherwise.
type Transform func(record.Record) (record.Record, error)

var _ processor.Processor = (Transform)(nil)

func (t Transform) Execute(_ context.Context, record record.Record) (record.Record, error) {
	return t(record)
}

// Config holds configuration data for building a transform.
type Config map[string]string

// NewBuilder is a utility function for creating a processor.Builder for transforms.
func NewBuilder(b func(Config) (Transform, error)) processor.Builder {
	return func(config processor.Config) (processor.Processor, error) {
		return b(config.Settings)
	}
}
