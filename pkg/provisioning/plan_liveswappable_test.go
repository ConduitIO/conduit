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

package provisioning

import (
	"testing"

	"github.com/matryer/is"
)

// TestChange_liveSwappable covers every (Resource, Action) combination plus the
// pipeline-update ConfigPaths cases (name/description swappable; dlq/connectors/
// processors not), which is the exact classification the live-apply decision
// depends on.
func TestChange_liveSwappable(t *testing.T) {
	testCases := []struct {
		name string
		ch   Change
		want bool
	}{
		// processors: an update is live-swappable, EXCEPT a Workers change (a
		// node-topology change a swap can't make — regression for the review's
		// MAJOR-1)
		{"processor update (no paths)", Change{Resource: ResourceProcessor, Action: ChangeActionUpdate}, true},
		{"processor update settings", Change{Resource: ResourceProcessor, Action: ChangeActionUpdate, ConfigPaths: []string{"settings.field"}}, true},
		{"processor update plugin", Change{Resource: ResourceProcessor, Action: ChangeActionUpdate, ConfigPaths: []string{"plugin"}}, true},
		{"processor update condition", Change{Resource: ResourceProcessor, Action: ChangeActionUpdate, ConfigPaths: []string{"condition"}}, true},
		{"processor update workers", Change{Resource: ResourceProcessor, Action: ChangeActionUpdate, ConfigPaths: []string{"workers"}}, false},
		{"processor update settings+workers", Change{Resource: ResourceProcessor, Action: ChangeActionUpdate, ConfigPaths: []string{"settings.field", "workers"}}, false},
		{"processor create", Change{Resource: ResourceProcessor, Action: ChangeActionCreate}, false},
		{"processor delete", Change{Resource: ResourceProcessor, Action: ChangeActionDelete}, false},

		// pipeline updates: only name/description-only qualify
		{"pipeline update name only", Change{Resource: ResourcePipeline, Action: ChangeActionUpdate, ConfigPaths: []string{"name"}}, true},
		{"pipeline update description only", Change{Resource: ResourcePipeline, Action: ChangeActionUpdate, ConfigPaths: []string{"description"}}, true},
		{"pipeline update name+description", Change{Resource: ResourcePipeline, Action: ChangeActionUpdate, ConfigPaths: []string{"name", "description"}}, true},
		{"pipeline update dlq", Change{Resource: ResourcePipeline, Action: ChangeActionUpdate, ConfigPaths: []string{"dlq"}}, false},
		{"pipeline update name+dlq", Change{Resource: ResourcePipeline, Action: ChangeActionUpdate, ConfigPaths: []string{"name", "dlq"}}, false},
		{"pipeline update connectors", Change{Resource: ResourcePipeline, Action: ChangeActionUpdate, ConfigPaths: []string{"connectors"}}, false},
		{"pipeline update processors", Change{Resource: ResourcePipeline, Action: ChangeActionUpdate, ConfigPaths: []string{"processors"}}, false},
		{"pipeline update no paths", Change{Resource: ResourcePipeline, Action: ChangeActionUpdate}, true}, // vacuously name/description-only
		{"pipeline create", Change{Resource: ResourcePipeline, Action: ChangeActionCreate}, false},
		{"pipeline delete", Change{Resource: ResourcePipeline, Action: ChangeActionDelete}, false},

		// connectors are never live-swappable (position/connection/ack state)
		{"connector update", Change{Resource: ResourceConnector, Action: ChangeActionUpdate}, false},
		{"connector create", Change{Resource: ResourceConnector, Action: ChangeActionCreate}, false},
		{"connector delete", Change{Resource: ResourceConnector, Action: ChangeActionDelete}, false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(tc.ch.liveSwappable(), tc.want)
		})
	}
}

// TestDiff_LiveEligible proves the all-or-nothing rule: a diff applies in place
// only if it is non-empty and every change is live-swappable.
func TestDiff_LiveEligible(t *testing.T) {
	procUpdate := Change{Resource: ResourceProcessor, Action: ChangeActionUpdate, LiveSwappable: true}
	nameUpdate := Change{Resource: ResourcePipeline, Action: ChangeActionUpdate, ConfigPaths: []string{"name"}, LiveSwappable: true}
	connUpdate := Change{Resource: ResourceConnector, Action: ChangeActionUpdate, LiveSwappable: false}
	dlqUpdate := Change{Resource: ResourcePipeline, Action: ChangeActionUpdate, ConfigPaths: []string{"dlq"}, LiveSwappable: false}

	testCases := []struct {
		name string
		diff Diff
		want bool
	}{
		{"empty", Diff{}, false},
		{"single processor update", Diff{Changes: []Change{procUpdate}}, true},
		{"processor + name updates", Diff{Changes: []Change{procUpdate, nameUpdate}}, true},
		{"single connector update", Diff{Changes: []Change{connUpdate}}, false},
		{"processor + connector (mixed)", Diff{Changes: []Change{procUpdate, connUpdate}}, false},
		{"processor + dlq (mixed)", Diff{Changes: []Change{procUpdate, dlqUpdate}}, false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			is.Equal(tc.diff.LiveEligible(), tc.want)
		})
	}
}
