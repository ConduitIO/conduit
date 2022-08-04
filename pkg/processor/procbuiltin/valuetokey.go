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

package procbuiltin

import (
	"context"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	valueToKeyName         = "valuetokey"
	valueToKeyConfigFields = "fields"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(valueToKeyName, ValueToKey)
}

// ValueToKey builds a processor that replaces the record key with a new key
// formed from a subset of fields in the record value.
//  * If the payload is raw and has a schema attached, the created key will also
//    have a schema with a subset of fields.
//  * If the payload is structured, the created key will also be structured with
//    a subset of fields.
//  * If the payload is raw and has no schema, return an error.
func ValueToKey(config processor.Config) (processor.Interface, error) {
	if config.Settings[valueToKeyConfigFields] == "" {
		return nil, cerrors.Errorf("%s: unspecified field %q", valueToKeyName, valueToKeyConfigFields)
	}

	fields := strings.Split(config.Settings[valueToKeyConfigFields], ",")

	return processor.InterfaceFunc(func(_ context.Context, r record.Record) (_ record.Record, err error) {
		defer func() {
			if err != nil {
				err = cerrors.Errorf("%s: %w", valueToKeyName, err)
			}
		}()

		switch d := r.Payload.After.(type) {
		case record.StructuredData:
			key := record.StructuredData{}
			for _, f := range fields {
				key[f] = d[f]
			}
			r.Key = key
			return r, nil
		case record.RawData:
			return record.Record{}, cerrors.ErrNotImpl
		default:
			return record.Record{}, cerrors.Errorf("unexpected payload type %T", r.Payload)
		}
	}), nil
}
