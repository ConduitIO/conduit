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

package registry

import (
	"fmt"
	"sort"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/registry/index"
	"github.com/conduitio/conduit/pkg/registry/trust"
)

// ResolveOptions is Resolve's input: which connector, which version (or
// none, for newest-compatible), and the running build's own versions to
// check compatibility against.
type ResolveOptions struct {
	// Name is looked up by EXACT match only — plan-v2/the design doc's
	// explicit anti-typosquat stance: a near-miss name (e.g. "postgress")
	// is CodeConnectorNotFound, never a fuzzy suggestion toward "postgres".
	Name string
	// Version is a version constraint string ("0.14.1" or "v0.14.1",
	// compared via NormalizeVersion — never raw string equality). Empty
	// means "newest version compatible with RunningConduitVersion/
	// RunningProtocolVersion".
	Version string

	// RunningConduitVersion/RunningProtocolVersion are compared against
	// each candidate ConnectorVersion's MinConduitVersion/
	// MinProtocolVersion. A value that does not parse as semver (e.g. Go's
	// own "development" build-info fallback for a locally built binary) is
	// treated as satisfying every compatibility check — a local dev build
	// must not hard-refuse every install just because it has no embedded
	// version.
	RunningConduitVersion  string
	RunningProtocolVersion string
}

// ResolvedVersion bundles what platform selection and the rest of the
// install pipeline need together: the connector (for its Publisher identity
// pin and Name) and the one ConnectorVersion that was selected.
type ResolvedVersion struct {
	Connector index.Connector
	Version   index.ConnectorVersion
}

// Resolve finds the connector named opts.Name in payload and selects one of
// its versions per opts.Version (exact match) or, if empty, the newest
// version compatible with the running build. It refuses (with a specific,
// coded reason) a connector whose publisher identity has been revoked, a
// pinned version that has been yanked, and an explicit version pin that is
// incompatible with the running Conduit/protocol version — but does NOT
// refuse a deprecated version (deprecated is a soft, informational flag,
// not a trust or safety state; see ResolvedVersion.Version.Deprecated,
// surfaced by the caller in --json/output). This mirrors the frozen index
// schema's own modeling: schemaVersion the parser is strict; deprecated is
// documented as orthogonal to trust, and no error code exists in the
// canonical table (plan-v2 §4) for refusing on it.
//
// Resolve never mutates payload and performs no I/O — it is a pure function
// over an already-fetched (and, in a real build, already-verified) index.
func Resolve(payload index.Payload, opts ResolveOptions) (*ResolvedVersion, error) {
	conn, err := findConnector(payload, opts.Name)
	if err != nil {
		return nil, err
	}

	if conn.Publisher.Revoked != nil {
		err := conduiterr.New(trust.CodeIdentityRevoked, fmt.Sprintf(
			"connector %q's publisher identity has been revoked: %s", opts.Name, conn.Publisher.Revoked.Reason))
		err.Suggestion = "every version of this connector is refused regardless of individual yank status; see the registry security advisory referenced in the revocation reason"
		return nil, err
	}

	if opts.Version != "" {
		return resolveExact(conn, opts)
	}
	return resolveNewestCompatible(conn, opts)
}

// findConnector does the exact-match name lookup. No Levenshtein/fuzzy
// suggestion is computed here, deliberately: a first registration is the
// one place typosquatting has no cryptographic backstop (plan-v2 §10), so
// this pipeline must never nudge a typo toward an existing name.
func findConnector(payload index.Payload, name string) (*index.Connector, error) {
	for i := range payload.Connectors {
		if payload.Connectors[i].Name == name {
			return &payload.Connectors[i], nil
		}
	}
	err := conduiterr.New(CodeConnectorNotFound, fmt.Sprintf(
		"connector %q not found in the registry index", name))
	err.Suggestion = "check the exact spelling — this is an exact-match lookup with no fuzzy suggestion, by design"
	return nil, err
}

func resolveExact(conn *index.Connector, opts ResolveOptions) (*ResolvedVersion, error) {
	wantV, err := NormalizeVersion(opts.Version)
	if err != nil {
		return nil, conduiterr.Wrap(CodeVersionNotFound,
			fmt.Sprintf("connector %q: %q is not a valid version", conn.Name, opts.Version), err)
	}

	for i := range conn.Versions {
		v := conn.Versions[i]
		gotV, err := NormalizeVersion(v.Version)
		if err != nil {
			continue // a malformed index entry; skip defensively rather than fail the whole resolution
		}
		if !gotV.Equal(wantV) {
			continue
		}

		if v.Yanked != nil {
			e := conduiterr.New(index.CodeVersionYanked, fmt.Sprintf(
				"connector %q version %s was yanked: %s", conn.Name, v.Version, v.Yanked.Reason))
			e.Suggestion = "install a different version, or omit @version to auto-select the newest non-yanked, compatible one"
			return nil, e
		}
		if err := checkCompatible(conn.Name, v, opts); err != nil {
			return nil, err
		}
		return &ResolvedVersion{Connector: *conn, Version: v}, nil
	}

	return nil, conduiterr.New(CodeVersionNotFound, fmt.Sprintf(
		"connector %q has no version %q in the registry index", conn.Name, opts.Version))
}

func resolveNewestCompatible(conn *index.Connector, opts ResolveOptions) (*ResolvedVersion, error) {
	versions := append([]index.ConnectorVersion(nil), conn.Versions...)
	sort.Slice(versions, func(i, j int) bool {
		vi, ei := NormalizeVersion(versions[i].Version)
		vj, ej := NormalizeVersion(versions[j].Version)
		if ei != nil || ej != nil {
			return false // malformed entries: leave relative order alone rather than guess
		}
		return vi.GreaterThan(vj) // descending: newest first
	})

	for _, v := range versions {
		if v.Yanked != nil {
			continue // never auto-select a yanked version
		}
		if err := checkCompatible(conn.Name, v, opts); err != nil {
			continue // not compatible with the running build; try the next-newest
		}
		return &ResolvedVersion{Connector: *conn, Version: v}, nil
	}

	err := conduiterr.New(CodeIncompatibleVersion, fmt.Sprintf(
		"no version of %q is compatible with the running Conduit (conduit %s, protocol %s)",
		conn.Name, opts.RunningConduitVersion, opts.RunningProtocolVersion))
	err.Suggestion = "upgrade Conduit, or pin an older @version explicitly if you know one is compatible"
	return nil, err
}

// checkCompatible refuses a candidate version whose MinConduitVersion or
// MinProtocolVersion exceeds the running build's own versions.
func checkCompatible(name string, v index.ConnectorVersion, opts ResolveOptions) error {
	if err := checkMinVersion("minConduitVersion", v.MinConduitVersion, opts.RunningConduitVersion); err != nil {
		return newIncompatibleError(name, v, opts)
	}
	if err := checkMinVersion("minProtocolVersion", v.MinProtocolVersion, opts.RunningProtocolVersion); err != nil {
		return newIncompatibleError(name, v, opts)
	}
	return nil
}

// checkMinVersion reports whether running satisfies >= minVer. An
// unparsable running version (e.g. "development") is treated as satisfying
// every check — see ResolveOptions's doc for why.
func checkMinVersion(label, minVer, running string) error {
	minV, err := NormalizeVersion(minVer)
	if err != nil {
		return fmt.Errorf("index entry has an invalid %s %q: %w", label, minVer, err)
	}
	runV, err := NormalizeVersion(running)
	if err != nil {
		return nil // unparsable running version (dev build): treat as compatible
	}
	if runV.LessThan(minV) {
		return fmt.Errorf("%s %s not satisfied by running %s", label, minVer, running)
	}
	return nil
}

// newIncompatibleError builds the actionable CodeIncompatibleVersion error:
// required vs. running versions, plus a concrete fix suggestion — per the
// CLAUDE.md "errors are API" convention (a stable code, the failing
// config-shaped fact, and a suggested fix). Returns error (not the
// concrete *conduiterr.ConduitError type) per this codebase's forbidigo
// convention — the type is reached via New/Wrap, never named directly
// outside the conduiterr package.
func newIncompatibleError(name string, v index.ConnectorVersion, opts ResolveOptions) error {
	err := conduiterr.New(CodeIncompatibleVersion, fmt.Sprintf(
		"connector %q version %s requires Conduit >= %s and protocol >= %s; running Conduit %s, protocol %s",
		name, v.Version, v.MinConduitVersion, v.MinProtocolVersion, opts.RunningConduitVersion, opts.RunningProtocolVersion))
	err.Suggestion = fmt.Sprintf(
		"upgrade Conduit to >= %s, or install an older %s version compatible with your running Conduit (omit @version to auto-select one)",
		v.MinConduitVersion, name)
	return err
}
