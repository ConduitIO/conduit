// Copyright © 2024 Meroxa, Inc.
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

package toschema

import (
	"github.com/conduitio/conduit-commons/schema"
	"github.com/twmb/franz-go/pkg/sr"
)

func SrSubjectSchema(s sr.SubjectSchema) schema.Schema {
	return schema.Schema{
		Subject: s.Subject,
		Version: s.Version,
		ID:      s.ID,
		Type:    SrSchemaType(s.Schema.Type),
		Bytes:   []byte(s.Schema.Schema),
	}
}

// SrSchema only partially populates schema.Schema, as it doesn't contain
// information about the id, subject and version. The schema can still be used
// to marshal and unmarshal data.
func SrSchema(s sr.Schema) schema.Schema {
	return schema.Schema{
		Subject: "",
		Version: 0,
		ID:      0,
		Type:    SrSchemaType(s.Type),
		Bytes:   []byte(s.Schema),
	}
}

func SrSchemaType(t sr.SchemaType) schema.Type {
	switch t {
	case sr.TypeAvro:
		return schema.TypeAvro
	case sr.TypeProtobuf:
		return 0 // not supported yet
	case sr.TypeJSON:
		return 0 // not supported yet
	default:
		return 0 // unknown
	}
}
