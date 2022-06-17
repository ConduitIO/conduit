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

package filter

import (
	"context"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

// todo add docs
type Filter func(record.Record) (record.Record, error)

var _ processor.Processor = (Filter)(nil)

func (f Filter) Execute(_ context.Context, record record.Record) (record.Record, error) {
	return f(record)
}
func (f Filter) Type() processor.Type {
	return processor.TypeFilter
}
