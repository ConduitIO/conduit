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

package config

import (
	"reflect"
	"sort"
	"testing"

	"github.com/matryer/is"
)

func TestPipelineFields(t *testing.T) {
	is := is.New(t)

	wantFields := extractFieldNames(reflect.TypeOf(Pipeline{}))
	haveFields := []string{"ID"} // ID is special, it's the identifier
	haveFields = append(haveFields, PipelineMutableFields...)
	haveFields = append(haveFields, PipelineIgnoredFields...)

	sort.Strings(wantFields)
	sort.Strings(haveFields)

	is.Equal(wantFields, haveFields) // fields don't match, if you added a field to Pipeline please add it to PipelineMutableFields or PipelineIgnoredFields
}

func TestConnectorFields(t *testing.T) {
	is := is.New(t)

	wantFields := extractFieldNames(reflect.TypeOf(Connector{}))
	haveFields := []string{"ID"} // ID is special, it's the identifier
	haveFields = append(haveFields, ConnectorImmutableFields...)
	haveFields = append(haveFields, ConnectorMutableFields...)

	sort.Strings(wantFields)
	sort.Strings(haveFields)

	is.Equal(wantFields, haveFields) // fields don't match, if you added a field to Connector please add it to ConnectorImmutableFields or ConnectorMutableFields
}

func extractFieldNames(t reflect.Type) []string {
	fields := make([]string, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		fields[i] = t.Field(i).Name
	}
	return fields
}
