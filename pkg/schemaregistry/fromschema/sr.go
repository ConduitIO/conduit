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

package fromschema

import (
	"github.com/conduitio/conduit-commons/schema"
	"github.com/twmb/franz-go/pkg/sr"
)

func SrSubjectSchema(s schema.Schema) sr.SubjectSchema {
	return sr.SubjectSchema{
		Subject: s.Subject,
		Version: s.Version,
		ID:      s.ID,
		Schema:  SrSchema(s),
	}
}

func SrSchema(s schema.Schema) sr.Schema {
	return sr.Schema{
		Schema:         string(s.Bytes),
		Type:           SrSchemaType(s.Type),
		References:     nil,
		SchemaMetadata: nil,
		SchemaRuleSet:  nil,
	}
}

func SrSchemaType(t schema.Type) sr.SchemaType {
	switch t {
	case schema.TypeAvro:
		return sr.TypeAvro
	default:
		return sr.SchemaType(-1) // unknown
	}
}
