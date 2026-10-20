// Copyright © 2026 Meroxa, Inc.
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

package internal

import (
	"context"

	"github.com/conduitio/conduit-commons/schema"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/toschema"
)

// RegistryResolver resolves Protobuf schema references through the schema
// registry. It is the single Resolver identity for the protobuf.decode
// processor: every pipeline in this process resolves references through the
// same registry, as schema.Resolver requires.
type RegistryResolver struct {
	registry schemaregistry.Registry
}

func NewRegistryResolver(registry schemaregistry.Registry) *RegistryResolver {
	return &RegistryResolver{registry: registry}
}

// ResolveReference fetches the referenced schema by subject and version and
// checks that it is a Protobuf schema. It must not call SerdeWithResolver for
// the schema being parsed, and it does not.
func (r *RegistryResolver) ResolveReference(ctx context.Context, ref schema.Reference) (schema.Schema, error) {
	ss, err := r.registry.SchemaBySubjectVersion(ctx, ref.Subject, ref.Version)
	if err != nil {
		return schema.Schema{}, cerrors.Errorf("failed to resolve reference %q (%s:%d): %w", ref.Name, ref.Subject, ref.Version, err)
	}

	sch := toschema.SrSubjectSchema(ss)
	if sch.Type != schema.TypeProtobuf {
		return schema.Schema{}, cerrors.Errorf("reference %q (%s:%d) resolved to schema type %s, want protobuf", ref.Name, ref.Subject, ref.Version, sch.Type)
	}

	return sch, nil
}
