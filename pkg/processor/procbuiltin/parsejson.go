// Copyright © 2023 Meroxa, Inc.
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
	"encoding/json"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	parseJSONKeyProcType     = "parsejsonkey"
	parseJSONPayloadProcType = "parsejsonpayload"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(parseJSONKeyProcType, ParseJSONKey)
	processor.GlobalBuilderRegistry.MustRegister(parseJSONPayloadProcType, ParseJSONPayload)
}

// ParseJSONKey parses the record key from raw to structured data
func ParseJSONKey(_ processor.Config) (processor.Interface, error) {
	return parseJSON(parseJSONKeyProcType, recordKeyGetSetter{})
}

// ParseJSONPayload parses the record payload from raw to structured data
func ParseJSONPayload(_ processor.Config) (processor.Interface, error) {
	return parseJSON(parseJSONPayloadProcType, recordPayloadGetSetter{})
}

func parseJSON(
	processorType string,
	getSetter recordDataGetSetter,
) (processor.Interface, error) {
	return NewFuncWrapper(func(_ context.Context, r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		switch data.(type) {
		case record.RawData:
			var jsonData record.StructuredData
			if len(data.Bytes()) == 0 {
				// change empty raw data to empty structured data
				r = getSetter.Set(r, jsonData)
				return r, nil
			}
			err := json.Unmarshal(data.Bytes(), &jsonData)
			if err != nil {
				return record.Record{}, cerrors.Errorf("%s: failed to unmarshal raw data as JSON: %w", processorType, err)
			}
			r = getSetter.Set(r, jsonData)

		case record.StructuredData:
			// data is already structured
		case nil:
			// if the field is nil leave it as it is
		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", processorType, data)
		}

		return r, nil
	}), nil
}
