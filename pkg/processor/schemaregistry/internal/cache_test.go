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
	"sync/atomic"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/lovromazgon/franz-go/pkg/sr"
	"github.com/matryer/is"
)

func TestSchemaCache_GetByID(t *testing.T) {
	is := is.New(t)
	cache := &SchemaCache{}

	id := 1
	want := sr.Schema{
		Schema:     `"string"`,
		Type:       sr.TypeAvro,
		References: nil,
	}
	var missCount atomic.Int32
	got, err := cache.GetByID(id, func() (sr.Schema, error) {
		missCount.Add(1)
		return want, nil
	})

	is.NoErr(err)
	is.Equal(want, got)
	is.Equal(missCount.Load(), int32(1))

	t.Run("GetByID is cached", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetByID(id, nil)
		is.NoErr(err)
		is.Equal(want, got)
		is.Equal(missCount.Load(), int32(1))
	})

	// other methods can't be cached, as the subject and version are unknown
}

func TestSchemaCache_GetBySubjectText(t *testing.T) {
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

	is := is.New(t)
	var missCount atomic.Int32
	got, err := cache.GetBySubjectText(want.Subject, want.Schema.Schema, func() (sr.SubjectSchema, error) {
		missCount.Add(1)
		return want, nil
	})
	is.NoErr(err)
	is.Equal(got, want)
	is.Equal(missCount.Load(), int32(1))

	t.Run("GetByID is cached", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetByID(want.ID, nil)
		is.NoErr(err)
		is.Equal(want.Schema, got)
		is.Equal(missCount.Load(), int32(1))
	})

	t.Run("GetBySubjectText is cached", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetBySubjectText(want.Subject, want.Schema.Schema, nil)
		is.NoErr(err)
		is.Equal(want, got)
		is.Equal(missCount.Load(), int32(1))
	})

	t.Run("GetBySubjectVersion is cached", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetBySubjectVersion(want.Subject, want.Version, nil)
		is.NoErr(err)
		is.Equal(want, got)
		is.Equal(missCount.Load(), int32(1))
	})
}

func TestSchemaCache_GetBySubjectVersion(t *testing.T) {
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

	is := is.New(t)
	var missCount atomic.Int32
	got, err := cache.GetBySubjectVersion(want.Subject, want.Version, func() (sr.SubjectSchema, error) {
		missCount.Add(1)
		return want, nil
	})
	is.NoErr(err)
	is.Equal(got, want)
	is.Equal(missCount.Load(), int32(1))

	t.Run("GetByID is cached", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetByID(want.ID, nil)
		is.NoErr(err)
		is.Equal(want.Schema, got)
		is.Equal(missCount.Load(), int32(1))
	})

	t.Run("GetBySubjectText is cached", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetBySubjectText(want.Subject, want.Schema.Schema, nil)
		is.NoErr(err)
		is.Equal(want, got)
		is.Equal(missCount.Load(), int32(1))
	})

	t.Run("GetBySubjectVersion is cached", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetBySubjectVersion(want.Subject, want.Version, nil)
		is.NoErr(err)
		is.Equal(want, got)
		is.Equal(missCount.Load(), int32(1))
	})
}

func TestSchemaCache_Miss(t *testing.T) {
	cache := &SchemaCache{}
	var want sr.SubjectSchema
	wantErr := cerrors.New("test error")

	t.Run("GetByID", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetByID(1, func() (sr.Schema, error) {
			return sr.Schema{}, wantErr
		})
		is.Equal(err, wantErr)
		is.Equal(want.Schema, got)
	})

	t.Run("GetBySubjectVersion", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetBySubjectVersion("test", 1, func() (sr.SubjectSchema, error) {
			return sr.SubjectSchema{}, wantErr
		})
		is.Equal(err, wantErr)
		is.Equal(want, got)
	})

	t.Run("GetBySubjectText", func(t *testing.T) {
		is := is.New(t)
		got, err := cache.GetBySubjectText("foo", `"string"`, func() (sr.SubjectSchema, error) {
			return sr.SubjectSchema{}, wantErr
		})
		is.Equal(err, wantErr)
		is.Equal(want, got)
	})
}
