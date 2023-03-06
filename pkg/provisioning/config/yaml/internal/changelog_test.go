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
)

func TestExpandChangelog(t *testing.T) {
	is := is.New(t)

	have := Changelog{
		"1.0": {}, // no changes, initial version
		"1.1": {{
			// introduced field should get a warning in all previous versions
			Field:      "pipelines.*.dead-letter-queue",
			ChangeType: FieldIntroduced,
			Message:    "field dead-letter-queue was introduced in version 1.1, please update the pipeline config version",
		}},
		"1.2": {{
			// introduced field should produce a warning in all previous
			// versions except those where a parent field itself was introduced,
			// in this case pipelines.*.dead-letter-queue takes precedence in 1.0
			Field:      "pipelines.*.dead-letter-queue.new-setting",
			ChangeType: FieldIntroduced,
			Message:    "field new-setting was introduced in version 1.2, please update the pipeline config version",
		}, {
			Field:      "pipelines.*.title",
			ChangeType: FieldIntroduced,
			Message:    "field title was introduced in version 1.2, please update the pipeline config version",
		}, {
			// deprecated field should show up in this and all newer versions,
			// except those that deprecate a parent field, in this case
			// pipelines was deprecated in 1.4 so it takes precedence
			Field:      "pipelines.*.name",
			ChangeType: FieldDeprecated,
			Message:    "field name was deprecated in version 1.2, please use \"title\" instead",
		}},
		"1.3": {}, // here just to test deprecated field
		"1.4": {{
			Field:      "pipelines",
			ChangeType: FieldDeprecated,
			Message:    "field pipelines was deprecated in version 1.4, please don't create pipelines anymore",
		}},
	}

	want := map[string]map[string]any{
		"1.0": {
			"pipelines": map[string]any{
				"*": map[string]any{
					"dead-letter-queue": Change{
						Field:      "pipelines.*.dead-letter-queue",
						ChangeType: FieldIntroduced,
						Message:    "field dead-letter-queue was introduced in version 1.1, please update the pipeline config version",
					},
					"title": Change{
						Field:      "pipelines.*.title",
						ChangeType: FieldIntroduced,
						Message:    "field title was introduced in version 1.2, please update the pipeline config version",
					},
				},
			},
		},
		"1.1": {
			"pipelines": map[string]any{
				"*": map[string]any{
					"dead-letter-queue": map[string]any{
						"new-setting": Change{
							Field:      "pipelines.*.dead-letter-queue.new-setting",
							ChangeType: FieldIntroduced,
							Message:    "field new-setting was introduced in version 1.2, please update the pipeline config version",
						},
					},
					"title": Change{
						Field:      "pipelines.*.title",
						ChangeType: FieldIntroduced,
						Message:    "field title was introduced in version 1.2, please update the pipeline config version",
					},
				},
			},
		},
		"1.2": {
			"pipelines": map[string]any{
				"*": map[string]any{
					"name": Change{
						Field:      "pipelines.*.name",
						ChangeType: FieldDeprecated,
						Message:    "field name was deprecated in version 1.2, please use \"title\" instead",
					},
				},
			},
		},
		"1.3": {
			"pipelines": map[string]any{
				"*": map[string]any{
					"name": Change{
						Field:      "pipelines.*.name",
						ChangeType: FieldDeprecated,
						Message:    "field name was deprecated in version 1.2, please use \"title\" instead",
					},
				},
			},
		},
		"1.4": {
			"pipelines": Change{
				Field:      "pipelines",
				ChangeType: FieldDeprecated,
				Message:    "field pipelines was deprecated in version 1.4, please don't create pipelines anymore",
			},
		},
	}

	got := have.Expand()
	is.Equal(want, got)
}
