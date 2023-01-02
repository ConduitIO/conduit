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

package provisioning

import (
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// changelog should be adjusted every time we change the pipeline config and add
// a new config version. Based on the changelog the parser will output warnings.
var changelog = map[string][]change{
	"1.0": {}, // no changes, initial version
	"1.1": {{
		field:      "pipelines.*.dead-letter-queue",
		changeType: fieldIntroduced,
		message:    "field dead-letter-queue was introduced in version 1.1, please update the pipeline config version",
	}},
}

// change is a single change that was introduced in a specific version.
type change struct {
	// field contains the path to the field that was changed. Nested fields can
	// be represented with dots (e.g. nested.field)
	field      string
	changeType changeType
	// message is the log message that will be printed if a file is detected
	// that uses this field with an unsupported version.
	message string
}

// changeType defines the type of the change introduced in a specific version.
type changeType int

const (
	fieldDeprecated changeType = iota
	fieldIntroduced
)

// expandedChangelog is populated in init based on changelog. This map contains
// the changelog in a structure that is useful for traversing in configLinter.
var expandedChangelog map[string]map[string]interface{}

func init() {
	expandedChangelog = expandChangelog(changelog)
}

func expandChangelog(changelog map[string][]change) map[string]map[string]interface{} {
	var versions semver.Collection
	for k := range changelog {
		versions = append(versions, semver.MustParse(k))
	}
	sort.Sort(versions)

	knownChanges := make(map[string]map[string]interface{})
	for _, v := range versions {
		knownChanges[v.Original()] = make(map[string]interface{})
	}

	// addChange stores change c in map m by splitting c.field into multiple
	// tokens and storing it in m[token1][token2][...][tokenN]. If any map in
	// that hierarchy does not exist it is created. If any value in that
	// hierarchy exists and is _not_ a map it is _not_ replaced. This means that
	// changes related to parent fields take precedence over changes related to
	// child fields.
	addChange := func(c change, m map[string]interface{}) {
		tokens := strings.Split(c.field, ".")
		curMap := m
		for i, t := range tokens {
			if i == len(tokens)-1 {
				// last token, set it in the map
				curMap[t] = c
				break
			}
			raw, ok := curMap[t]
			if !ok {
				nextMap := make(map[string]interface{})
				curMap[t] = nextMap
				curMap = nextMap
				continue
			}
			curMap, ok = raw.(map[string]interface{})
			if !ok {
				break
			}
		}
	}

	for _, v := range versions {
		changes := changelog[v.Original()]
		for _, v2 := range versions {
			switch {
			case !v.GreaterThan(v2):
				// warn about deprecated fields in future versions
				for _, c := range changes {
					if c.changeType == fieldDeprecated {
						addChange(c, knownChanges[v2.Original()])
					}
				}
			case v.GreaterThan(v2):
				// warn about introduced fields in older versions
				for _, c := range changes {
					if c.changeType == fieldIntroduced {
						addChange(c, knownChanges[v2.Original()])
					}
				}
			}
		}
	}
	return knownChanges
}
