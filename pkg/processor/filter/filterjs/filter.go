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

package filterjs

import (
	"fmt"

	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/javascript"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/rs/zerolog"
)

// Config holds configuration data for building a transform.
type Config map[string]string

type Filter struct {
	jsFunc javascript.Function
}

func NewFilter(src string, logger zerolog.Logger) (Filter, error) {
	engine, err := javascript.NewFunction(src, "filter", logger)
	if err != nil {
		return Filter{}, fmt.Errorf("failed creating JavaScript function: %w", err)
	}
	return Filter{jsFunc: engine}, nil
}

func (f Filter) Filter(r record.Record) (record.Record, error) {
	res, err := f.jsFunc.Call(r)
	if err != nil {
		return record.Record{}, fmt.Errorf("failed calling filter function: %w", err)
	}
	val, ok := res.(bool)
	if !ok {
		return record.Record{}, fmt.Errorf("filter function returned %v instead of a bool", res)
	}
	if val {
		return record.Record{}, processor.ErrSkipRecord
	}
	return record.Record{}, nil
}
