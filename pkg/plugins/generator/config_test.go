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

package generator

import (
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"

	"github.com/conduitio/conduit/pkg/plugins"
)

func TestParseFull(t *testing.T) {
	underTest, err := Parse(plugins.Config{Settings: map[string]string{
		"recordCount": "-1",
		"readTime":    "5s",
		"fields":      "id:int,name:string,joined:time,admin:bool",
	}})
	assert.Ok(t, err)
	assert.Equal(t, int64(-1), underTest.RecordCount)
	assert.Equal(t, 5*time.Second, underTest.ReadTime)
	assert.Equal(t, map[string]string{"id": "int", "name": "string", "joined": "time", "admin": "bool"}, underTest.Fields)
}

func TestParseFields_RequiredNotPresent(t *testing.T) {
	_, err := Parse(plugins.Config{Settings: map[string]string{
		"recordCount": "100",
		"readTime":    "5s",
	}})
	assert.Error(t, err)
	assert.Equal(t, "no fields specified", err.Error())
}

func TestParseFields_OptionalNotPresent(t *testing.T) {
	_, err := Parse(plugins.Config{Settings: map[string]string{
		"fields": "a:int",
	}})
	assert.Ok(t, err)
}

func TestParseFields_MalformedFields_NoType(t *testing.T) {
	_, err := Parse(plugins.Config{Settings: map[string]string{
		"fields": "abc:",
	}})
	assert.Error(t, err)
	assert.Equal(t, `invalid field spec "abc:"`, err.Error())
}

func TestParseFields_MalformedFields_NameOnly(t *testing.T) {
	_, err := Parse(plugins.Config{Settings: map[string]string{
		"fields": "abc",
	}})
	assert.Error(t, err)
	assert.Equal(t, `invalid field spec "abc"`, err.Error())
}
