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

package txfbuiltin

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor/transform"
	"github.com/conduitio/conduit/pkg/record"
)

func getConfigField(c transform.Config, field string) (string, error) {
	val, ok := c[field]
	if !ok || val == "" {
		return "", cerrors.Errorf("empty config field %q", field)
	}
	return val, nil
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
