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

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// storeReachableCheck opens the configured database via conduit.OpenStore
// (the exact DB-open logic conduit.NewRuntime uses), pings it, and closes
// it — mirroring what `conduit run` does at startup without duplicating
// that logic.
//
// Opening a real database driver is inherently side-effecting for the
// file-backed drivers (badger, sqlite): badger.Open creates its directory
// and value-log files on first open, sqlite.New creates its file, neither
// removes anything on Close. That conflicts with doctor's "no side effects,
// nothing left behind" contract (see the design doc's acceptance criteria)
// for the common "path doesn't exist yet" case — running doctor before
// `conduit run` for the first time. Run resolves this by recording whether
// the configured path existed before it opened the store and, if it
// didn't, removing whatever got created once the probe is done. A
// pre-existing path (real pipeline state) is never touched — only an
// artifact doctor itself is responsible for creating gets cleaned up.
type storeReachableCheck struct {
	cfg    conduit.Config
	logger log.CtxLogger
}

func (c storeReachableCheck) Name() string { return NameStoreReachable }

func (c storeReachableCheck) Run(ctx context.Context) check.CheckResult {
	if c.cfg.DB.Driver == nil && c.cfg.DB.Type == conduit.DBTypeInMemory {
		return check.CheckResult{
			Status:   check.StatusWarn,
			Category: CategoryStore,
			Message:  "using an in-memory store — all pipeline configurations will be lost when Conduit stops",
		}
	}

	configPath, probePath := storeConfigPath(c.cfg)

	var existedBefore bool
	if probePath != "" {
		if _, err := os.Stat(probePath); err == nil {
			existedBefore = true
		}
	}

	db, err := conduit.OpenStore(c.cfg, c.logger)
	if err != nil {
		return check.CheckResult{
			Status:     check.StatusFail,
			Category:   CategoryStore,
			Code:       storeErrCode(err),
			ConfigPath: configPath,
			Message:    err.Error(),
			Suggestion: storeSuggestion(c.cfg, configPath),
		}
	}
	defer func() {
		_ = db.Close()
		// Invariant: doctor leaves no side effects behind (see the type
		// doc) — only remove what this probe itself created.
		if probePath != "" && !existedBefore {
			_ = os.RemoveAll(probePath)
		}
	}()

	if err := db.Ping(ctx); err != nil {
		return check.CheckResult{
			Status:     check.StatusFail,
			Category:   CategoryStore,
			Code:       conduiterr.CodeUnavailable.Reason(),
			ConfigPath: configPath,
			Message:    fmt.Sprintf("opened the store but could not ping it: %v", err),
			Suggestion: storeSuggestion(c.cfg, configPath),
		}
	}

	return check.CheckResult{
		Status:     check.StatusPass,
		Category:   CategoryStore,
		ConfigPath: configPath,
		Message:    "store is reachable",
	}
}

// storeErrCode picks the CheckResult's Code from an OpenStore error: an
// error OpenStore itself tagged (a real open/dial failure, Unavailable —
// environment, exit bucket 3) keeps that tag; an untagged error (currently
// only "invalid DB type", a config problem, not an environment one) is
// reported as InvalidArgument (validation, exit bucket 2) instead of
// falling through to a generic Unavailable, which would misclassify it.
func storeErrCode(err error) string {
	if ce, ok := conduiterr.Get(err); ok {
		return ce.Code.Reason()
	}
	return conduiterr.CodeInvalidArgument.Reason()
}

// storeConfigPath returns the conduit.yaml dotted key the configured store
// is sourced from, and — for the file-backed drivers only — the filesystem
// path storeReachableCheck probes for pre-existence so it can clean up
// after itself. probePath is empty for postgres (no filesystem artifact)
// and for a caller-supplied cfg.DB.Driver (unknown storage, not doctor's to
// manage).
func storeConfigPath(cfg conduit.Config) (configKey, probePath string) {
	if cfg.DB.Driver != nil {
		return "", ""
	}
	switch cfg.DB.Type {
	case conduit.DBTypeBadger:
		return "db.badger.path", cfg.DB.Badger.Path
	case conduit.DBTypePostgres:
		return "db.postgres.connection-string", ""
	case conduit.DBTypeSQLite:
		return "db.sqlite.path", cfg.DB.SQLite.Path
	default:
		return "db.type", ""
	}
}

func storeSuggestion(cfg conduit.Config, configPath string) string {
	switch cfg.DB.Type {
	case conduit.DBTypeBadger:
		return fmt.Sprintf("check that the directory for %s exists and is writable, or set %s to a different path", cfg.DB.Badger.Path, configPath)
	case conduit.DBTypeSQLite:
		return fmt.Sprintf("check that the directory for %s exists and is writable, or set %s to a different path", cfg.DB.SQLite.Path, configPath)
	case conduit.DBTypePostgres:
		return fmt.Sprintf("check the postgres connection string and that the server is reachable, or set %s", configPath)
	default:
		return "check the configured db.type and its settings"
	}
}
