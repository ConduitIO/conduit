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

package yaml

import (
	"testing"

	"github.com/matryer/is"
)

func TestExpandChangelog(t *testing.T) {
	is := is.New(t)

	have := map[string][]change{
		"1.0": {}, // no changes, initial version
		"1.1": {{
			// introduced field should get a warning in all previous versions
			field:      "pipelines.*.dead-letter-queue",
			changeType: fieldIntroduced,
			message:    "field dead-letter-queue was introduced in version 1.1, please update the pipeline config version",
		}},
		"1.2": {{
			// introduced field should produce a warning in all previous
			// versions except those where a parent field itself was introduced,
			// in this case pipelines.*.dead-letter-queue takes precedence in 1.0
			field:      "pipelines.*.dead-letter-queue.new-setting",
			changeType: fieldIntroduced,
			message:    "field new-setting was introduced in version 1.2, please update the pipeline config version",
		}, {
			field:      "pipelines.*.title",
			changeType: fieldIntroduced,
			message:    "field title was introduced in version 1.2, please update the pipeline config version",
		}, {
			// deprecated field should show up in this and all newer versions,
			// except those that deprecate a parent field, in this case
			// pipelines was deprecated in 1.4 so it takes precedence
			field:      "pipelines.*.name",
			changeType: fieldDeprecated,
			message:    "field name was deprecated in version 1.2, please use \"title\" instead",
		}},
		"1.3": {}, // here just to test deprecated field
		"1.4": {{
			field:      "pipelines",
			changeType: fieldDeprecated,
			message:    "field pipelines was deprecated in version 1.4, please don't create pipelines anymore",
		}},
	}

	want := map[string]map[string]interface{}{
		"1.0": {
			"pipelines": map[string]interface{}{
				"*": map[string]interface{}{
					"dead-letter-queue": change{
						field:      "pipelines.*.dead-letter-queue",
						changeType: fieldIntroduced,
						message:    "field dead-letter-queue was introduced in version 1.1, please update the pipeline config version",
					},
					"title": change{
						field:      "pipelines.*.title",
						changeType: fieldIntroduced,
						message:    "field title was introduced in version 1.2, please update the pipeline config version",
					},
				},
			},
		},
		"1.1": {
			"pipelines": map[string]interface{}{
				"*": map[string]interface{}{
					"dead-letter-queue": map[string]interface{}{
						"new-setting": change{
							field:      "pipelines.*.dead-letter-queue.new-setting",
							changeType: fieldIntroduced,
							message:    "field new-setting was introduced in version 1.2, please update the pipeline config version",
						},
					},
					"title": change{
						field:      "pipelines.*.title",
						changeType: fieldIntroduced,
						message:    "field title was introduced in version 1.2, please update the pipeline config version",
					},
				},
			},
		},
		"1.2": {
			"pipelines": map[string]interface{}{
				"*": map[string]interface{}{
					"name": change{
						field:      "pipelines.*.name",
						changeType: fieldDeprecated,
						message:    "field name was deprecated in version 1.2, please use \"title\" instead",
					},
				},
			},
		},
		"1.3": {
			"pipelines": map[string]interface{}{
				"*": map[string]interface{}{
					"name": change{
						field:      "pipelines.*.name",
						changeType: fieldDeprecated,
						message:    "field name was deprecated in version 1.2, please use \"title\" instead",
					},
				},
			},
		},
		"1.4": {
			"pipelines": change{
				field:      "pipelines",
				changeType: fieldDeprecated,
				message:    "field pipelines was deprecated in version 1.4, please don't create pipelines anymore",
			},
		},
	}

	got := expandChangelog(have)
	is.Equal(want, got)
}
