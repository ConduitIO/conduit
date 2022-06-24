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

package builtin

import (
	"context"
	"strconv"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

// ProcessorFunc is a stateless processor.Processor implementation
// which is using a Go function to process records.
// todo move out of this file.
type ProcessorFunc func(context.Context, record.Record) (record.Record, error)

func (p ProcessorFunc) Execute(ctx context.Context, record record.Record) (record.Record, error) {
	return p(ctx, record)
}

var errEmptyConfigField = cerrors.New("empty config field")

func getConfigFieldString(c processor.Config, field string) (string, error) {
	val, ok := c.Settings[field]
	if !ok || val == "" {
		return "", cerrors.Errorf("failed to retrieve config field %q: %w", field, errEmptyConfigField)
	}
	return val, nil
}

func getConfigFieldFloat64(c processor.Config, field string) (float64, error) {
	raw, err := getConfigFieldString(c, field)
	if err != nil {
		return 0, err
	}

	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, cerrors.Errorf("failed to parse %q as float64: %w", field, err)
	}

	return parsed, nil
}

func getConfigFieldInt64(c processor.Config, field string) (int64, error) {
	raw, err := getConfigFieldString(c, field)
	if err != nil {
		return 0, err
	}

	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, cerrors.Errorf("failed to parse %q as int64: %w", field, err)
	}

	return parsed, nil
}

func getConfigFieldDuration(c processor.Config, field string) (time.Duration, error) {
	raw, err := getConfigFieldString(c, field)
	if err != nil {
		return 0, err
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, cerrors.Errorf("failed to parse %q as time.Duration: %w", field, err)
	}

	return parsed, nil
}

// recordDataGetSetter is a utility that returns either the key or the payload
// data. It provides also a function to set the key or payload data.
// It is useful when writing 2 transforms that do the same thing, except that
// one operates on the key and the other on the payload.
type recordDataGetSetter interface {
	Get(record.Record) record.Data
	Set(record.Record, record.Data) record.Record
}

type recordPayloadGetSetter struct{}

func (recordPayloadGetSetter) Get(r record.Record) record.Data {
	return r.Payload
}
func (recordPayloadGetSetter) Set(r record.Record, d record.Data) record.Record {
	r.Payload = d
	return r
}

type recordKeyGetSetter struct{}

func (recordKeyGetSetter) Get(r record.Record) record.Data {
	return r.Key
}
func (recordKeyGetSetter) Set(r record.Record, d record.Data) record.Record {
	r.Key = d
	return r
}
