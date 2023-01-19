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
	parseJsonKeyProcType     = "parsejsonkey"
	parseJsonPayloadProcType = "parsejsonpayload"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(parseJsonKeyProcType, ParseJsonKey)
	processor.GlobalBuilderRegistry.MustRegister(parseJsonPayloadProcType, ParseJsonPayload)
}

// ParseJsonKey parses the record key from raw to structured data
func ParseJsonKey(config processor.Config) (processor.Interface, error) {
	return parseJson(parseJsonKeyProcType, recordKeyGetSetter{}, config)
}

// ParseJsonPayload parses the record payload from raw to structured data
func ParseJsonPayload(config processor.Config) (processor.Interface, error) {
	return parseJson(parseJsonPayloadProcType, recordPayloadGetSetter{}, config)
}

func parseJson(
	processorType string,
	getSetter recordDataGetSetter,
	config processor.Config,
) (processor.Interface, error) {

	return processor.InterfaceFunc(func(_ context.Context, r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		switch data.(type) {
		case record.RawData:
			var jsonData record.StructuredData
			err := json.Unmarshal(data.Bytes(), &jsonData)
			if err != nil {
				return record.Record{}, cerrors.Errorf("%s: %w", processorType, err)
			}
			r = getSetter.Set(r, jsonData)

		case record.StructuredData:
			return record.Record{}, cerrors.Errorf("%s: the data is already structured", processorType)

		default:
			return record.Record{}, cerrors.Errorf("%s: unexpected data type %T", processorType, data)
		}

		return r, nil
	}), nil
}
