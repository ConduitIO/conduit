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

package processor

import (
	"testing"

	"github.com/conduitio/conduit/pkg/record"
	"github.com/matryer/is"
)

func Test_TemplateUtils_ExecuteTrue(t *testing.T) {
	is := is.New(t)
	condition := "{{ eq .Metadata.key \"val\" }}"
	rec := record.Record{
		Position: record.Position("position-out"),
		Metadata: record.Metadata{"key": "val"},
	}
	tmpl := NewTemplateUtils(condition, rec)
	is.NoErr(tmpl.parse())
	res, err := tmpl.execute()
	is.NoErr(err)
	is.True(res)
}

func Test_TemplateUtils_ExecuteFalse(t *testing.T) {
	is := is.New(t)
	condition := "{{ eq .Metadata.key \"wrongVal\" }}"
	rec := record.Record{
		Position: record.Position("position-out"),
		Metadata: record.Metadata{"key": "val"},
	}
	tmpl := NewTemplateUtils(condition, rec)
	is.NoErr(tmpl.parse())
	res, err := tmpl.execute()
	is.NoErr(err)
	is.True(res == false)
}

func Test_TemplateUtils_NonBooleanValue(t *testing.T) {
	is := is.New(t)
	condition := "{{ printf \"hi\" }}"
	rec := record.Record{
		Position: record.Position("position-out"),
		Metadata: record.Metadata{"key": "val"},
	}
	tmpl := NewTemplateUtils(condition, rec)
	is.NoErr(tmpl.parse())
	res, err := tmpl.execute()
	is.True(err != nil)
	is.True(res == false)
}
