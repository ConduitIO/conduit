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

package doctorcheck

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// orphanedResourcesCheck reports connectors that were deleted with --force
// after their delete hook failed (#3058): resources they created outside
// Conduit, such as a Postgres replication slot that keeps WAL, may still
// exist. The entries are written by connector.Service.ForceDelete and stay
// until `conduit connectors delete ID --acknowledge-orphan` clears them.
//
// It reads the store offline, like storeReachableCheck, and follows the same
// rule of leaving nothing behind: a store path that does not exist yet is not
// opened at all (there can be no entries in it). A store that cannot be
// opened is a warning here, not a failure; store.reachable already reports
// why it can't be opened.
type orphanedResourcesCheck struct {
	cfg    conduit.Config
	logger log.CtxLogger
}

func (c orphanedResourcesCheck) Name() string { return NameConnectorsOrphanedResources }

func (c orphanedResourcesCheck) Run(ctx context.Context) check.CheckResult {
	if c.cfg.DB.Driver == nil && c.cfg.DB.Type == conduit.DBTypeInMemory {
		return check.CheckResult{
			Status:   check.StatusPass,
			Category: CategoryStore,
			Message:  "in-memory store: no orphaned connector resources can be recorded across runs",
		}
	}
	if _, probePath := storeConfigPath(c.cfg); probePath != "" {
		if _, err := os.Stat(probePath); err != nil {
			return check.CheckResult{
				Status:   check.StatusPass,
				Category: CategoryStore,
				Message:  "no store yet, so no orphaned connector resources",
			}
		}
	}

	db, err := conduit.OpenStore(ctx, c.cfg, c.logger)
	if err != nil {
		return check.CheckResult{
			Status:     check.StatusWarn,
			Category:   CategoryStore,
			Code:       storeErrCode(err),
			Message:    fmt.Sprintf("could not open the store to look for orphaned connector resources: %v", err),
			Suggestion: "see store.reachable; with BadgerDB or SQLite, stop the running Conduit server first so doctor can read the store",
		}
	}
	defer func() { _ = db.Close() }()

	orphans, err := connector.ListOrphans(ctx, db)
	if err != nil {
		return check.CheckResult{
			Status:   check.StatusWarn,
			Category: CategoryStore,
			Code:     connector.CodeConnectorOrphanedResources.Reason(),
			Message:  fmt.Sprintf("could not read the orphaned connector entries: %v", err),
		}
	}
	if len(orphans) == 0 {
		return check.CheckResult{
			Status:   check.StatusPass,
			Category: CategoryStore,
			Message:  "no connectors were deleted with --force after a failed delete hook",
		}
	}

	msgs := make([]string, len(orphans))
	ids := make([]string, len(orphans))
	for i, o := range orphans {
		msgs[i] = o.Warning().Message
		ids[i] = o.ConnectorID
	}
	return check.CheckResult{
		Status:   check.StatusWarn,
		Category: CategoryStore,
		Code:     connector.CodeConnectorOrphanedResources.Reason(),
		Message: fmt.Sprintf("%d connector(s) deleted with --force may have left resources outside Conduit: %s",
			len(orphans), strings.Join(msgs, "; ")),
		Suggestion: "check each external system for what the plugin's error names (for Postgres, " +
			"SELECT slot_name FROM pg_replication_slots), remove it by hand, then run " +
			"'conduit connectors delete <id> --acknowledge-orphan' for: " + strings.Join(ids, ", ") +
			" (see docs/operations/connector-delete-hook-failures.md)",
	}
}
