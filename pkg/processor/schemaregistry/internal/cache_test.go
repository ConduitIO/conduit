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

package internal

import (
	"testing"

	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

func TestSchemaCache_AddSubjectSchema(t *testing.T) {
	cache := &SchemaCache{}

	want := sr.SubjectSchema{
		Subject: "test",
		Version: 1,
		ID:      2,
		Schema: sr.Schema{
			Schema:     `"string"`,
			Type:       sr.TypeAvro,
			References: nil,
		},
	}
	cache.AddSubjectSchema(want)

	t.Run("GetByID", func(t *testing.T) {
		is := is.New(t)
		got, ok := cache.GetByID(want.ID)
		is.True(ok)
		is.Equal(want.Schema, got)
	})

	t.Run("GetBySubjectVersion", func(t *testing.T) {
		is := is.New(t)
		got, ok := cache.GetBySubjectVersion(want.Subject, want.Version)
		is.True(ok)
		is.Equal(want, got)
	})

	t.Run("GetBySubjectText", func(t *testing.T) {
		is := is.New(t)
		got, ok := cache.GetBySubjectText(want.Subject, want.Schema.Schema)
		is.True(ok)
		is.Equal(want, got)
	})
}

func TestSchemaCache_AddSchema(t *testing.T) {
	cache := &SchemaCache{}

	id := 1
	want := sr.Schema{
		Schema:     `"string"`,
		Type:       sr.TypeAvro,
		References: nil,
	}
	cache.AddSchema(id, want)

	is := is.New(t)
	got, ok := cache.GetByID(id)
	is.True(ok)
	is.Equal(want, got)
}

func TestSchemaCache_Miss(t *testing.T) {
	cache := &SchemaCache{}
	var want sr.SubjectSchema

	t.Run("GetByID", func(t *testing.T) {
		is := is.New(t)
		got, ok := cache.GetByID(1)
		is.True(!ok)
		is.Equal(want.Schema, got)
	})

	t.Run("GetBySubjectVersion", func(t *testing.T) {
		is := is.New(t)
		got, ok := cache.GetBySubjectVersion("test", 1)
		is.True(!ok)
		is.Equal(want, got)
	})

	t.Run("GetBySubjectText", func(t *testing.T) {
		is := is.New(t)
		got, ok := cache.GetBySubjectText("foo", `"string"`)
		is.True(!ok)
		is.Equal(want, got)
	})
}
