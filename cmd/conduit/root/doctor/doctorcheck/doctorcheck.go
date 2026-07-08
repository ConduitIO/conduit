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
	"os"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/conduitio/conduit/pkg/conduit/check"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

// Stable check names. These are what `conduit doctor --check <name>`
// filters on and what CheckNames enumerates for validation error messages —
// keep them in sync with the switch in DefaultChecks.
const (
	NameConfigResolve           = "config.resolve"
	NameConfigValidate          = "config.validate"
	NameStoreReachable          = "store.reachable"
	NameNetworkGRPC             = "network.grpc"
	NameNetworkHTTP             = "network.http"
	NamePluginsConnectorsDir    = "plugins.connectors_dir"
	NamePluginsProcessorsDir    = "plugins.processors_dir"
	NamePluginsBuiltin          = "plugins.builtin"
	NameEngineReachable         = "engine.reachable"
	NamePluginsStandaloneCompat = "plugins.standalone_compat"
)

// Categories doctor's own checks use for human-output grouping (see
// cmd/conduit/root/doctor's Render). check.CategoryNetwork is reused
// directly for the network.grpc/http checks (they delegate to
// check.AddrBindable, which already sets it) rather than redefined here.
const (
	CategoryConfig  = "config"
	CategoryStore   = "store"
	CategoryPlugins = "plugins"
	CategoryEngine  = "engine"
)

// conduit.yaml dotted config keys multiple checks reference as their
// CheckResult.ConfigPath — named once here so config.go, engine.go,
// standalone.go and this file can't drift on the exact string.
const (
	configKeyConfigPath     = "config.path"
	configKeyAPIGRPCAddress = "api.grpc.address"
	configKeyConnectorsPath = "connectors.path"
)

// Options configures DefaultChecks.
type Options struct {
	// Deep enables plugins.standalone_compat, which dispenses every
	// standalone connector plugin binary found in cfg.Connectors.Path in an
	// isolated subprocess to confirm it produces a valid specification. Off
	// by default: it is the only check here that executes external code,
	// and doing that safely costs a subprocess round trip per plugin.
	Deep bool
	// RequireServer makes engine.reachable a Fail (instead of a Warn) when
	// no Conduit server answers at cfg.API.GRPC.Address. Off by default:
	// "no server running yet" is the common case (doctor runs before
	// `conduit run`), not a problem to report as a failure.
	RequireServer bool
	// Executable is the path to the running conduit binary, used only by
	// the (--deep) standaloneCompatCheck to re-exec itself as the isolated
	// subprocess worker (see that file's doc). Defaults to os.Executable()
	// when empty; callers only need to set this in tests that stub the
	// worker.
	Executable string
}

// CheckNames returns the stable name of every check DefaultChecks can
// produce, regardless of Options — including plugins.standalone_compat,
// which only actually runs with Options.Deep set. This lets `--check`
// validation list the full set of valid names even when --deep wasn't
// passed (see NamePluginsStandaloneCompat's own --deep gating, enforced by
// the caller, not this package).
func CheckNames() []string {
	return []string{
		NameConfigResolve,
		NameConfigValidate,
		NameStoreReachable,
		NameNetworkGRPC,
		NameNetworkHTTP,
		NamePluginsConnectorsDir,
		NamePluginsProcessorsDir,
		NamePluginsBuiltin,
		NameEngineReachable,
		NamePluginsStandaloneCompat,
	}
}

// DefaultChecks returns doctor's check set for cfg: the runtime/environment
// checks that answer "would `conduit run` succeed here?" (see the package
// doc). logger is passed through to storeReachableCheck (which needs one to
// call conduit.OpenStore) — pass log.Nop() for a quiet run.
//
// The returned []check.Check is meant for check.Run; this function does no
// running itself and has no side effects of its own (the individual checks
// may, transiently — see their own docs).
func DefaultChecks(cfg conduit.Config, logger log.CtxLogger, opts Options) []check.Check {
	checks := []check.Check{
		configResolveCheck{cfg: cfg},
		configValidateCheck{cfg: cfg},
		storeReachableCheck{cfg: cfg, logger: logger},
	}

	if cfg.API.Enabled {
		checks = append(checks,
			addrCheck{name: NameNetworkGRPC, addr: cfg.API.GRPC.Address, configKey: configKeyAPIGRPCAddress},
			addrCheck{name: NameNetworkHTTP, addr: cfg.API.HTTP.Address, configKey: "api.http.address"},
		)
	} else {
		checks = append(checks,
			apiDisabledCheck{name: NameNetworkGRPC},
			apiDisabledCheck{name: NameNetworkHTTP},
		)
	}

	checks = append(checks,
		pluginsDirCheck{name: NamePluginsConnectorsDir, path: cfg.Connectors.Path, configKey: configKeyConnectorsPath},
		pluginsDirCheck{name: NamePluginsProcessorsDir, path: cfg.Processors.Path, configKey: "processors.path"},
		pluginsBuiltinCheck{cfg: cfg},
		engineReachableCheck{cfg: cfg, requireServer: opts.RequireServer},
	)

	if opts.Deep {
		exe := opts.Executable
		if exe == "" {
			if resolved, err := os.Executable(); err == nil {
				exe = resolved
			}
		}
		checks = append(checks, standaloneCompatCheck{connectorsDir: cfg.Connectors.Path, executable: exe})
	}

	return checks
}
