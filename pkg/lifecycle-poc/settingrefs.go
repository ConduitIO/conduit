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

package lifecycle

import (
	"context"

	"github.com/conduitio/conduit/pkg/pipeline"
)

// refuseSettingRefs marks the pipeline Degraded with the coded refusal from
// lifecyclev1.CheckSettingRefs. The caller holds the start reservation, so no
// run exists whose status this could overwrite. Stored records are not touched
// (Invariant 6).
func (s *Service) refuseSettingRefs(ctx context.Context, pl *pipeline.Instance, cause error) {
	s.logger.Warn(ctx).
		Err(cause).
		Str("pipeline_id", pl.ID).
		Str("code", pipeline.CodeSettingsReferencesUnsupported.Reason()).
		Msg("pipeline refused: stored settings use references written by a newer Conduit")

	unlock := s.lockStatus(pl.ID)
	defer unlock()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.runningWriteTimeout())
	defer cancel()
	if err := s.pipelines.UpdateStatus(wctx, pl.ID, pipeline.StatusDegraded,
		pipeline.CodeSettingsReferencesUnsupported.Reason()+": "+cause.Error()); err != nil {
		s.logger.Warn(ctx).Err(err).Str("pipeline_id", pl.ID).Msg("degraded status not persisted")
	}
}
