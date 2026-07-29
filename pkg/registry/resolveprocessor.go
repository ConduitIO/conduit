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

// ResolvedProcessorVersion is the processor analogue of ResolvedVersion: the
// Processor (for its pinned Publisher identity and Name) plus the single
// ProcessorVersion selected.
type ResolvedProcessorVersion struct {
	Processor index.Processor
	Version   index.ProcessorVersion
}

// ResolveProcessor is the processor analogue of Resolve, over
// payload.Processors. It applies IDENTICAL rules — exact-match name
// (anti-typosquat, no fuzzy suggestion), revoked publisher refused, a pinned
// yanked version refused, newest-non-yanked-compatible when @version is
// omitted, and incompatible min-versions refused — differing only in the
// collection it searches and the (processor-worded, processor-coded) errors it
// returns. Like Resolve it never mutates payload and performs no I/O.
//
// It deliberately does NOT refuse a deprecated version: deprecated is a soft,
// informational flag surfaced by the caller, not a trust or safety state —
// exactly as in Resolve (see resolve.go's doc for the rationale).
func ResolveProcessor(payload index.Payload, opts ResolveOptions) (*ResolvedProcessorVersion, error) {
	proc, err := findProcessor(payload, opts.Name)
	if err != nil {
		return nil, err
	}

	if proc.Publisher.Revoked != nil {
		e := conduiterr.New(trust.CodeIdentityRevoked, fmt.Sprintf(
			"processor %q's publisher identity has been revoked: %s", opts.Name, proc.Publisher.Revoked.Reason))
		e.Suggestion = "every version of this processor is refused regardless of individual yank status; see the registry security advisory referenced in the revocation reason"
		return nil, e
	}

	if opts.Version != "" {
		return resolveProcessorExact(proc, opts)
	}
	return resolveProcessorNewestCompatible(proc, opts)
}

// findProcessor does the exact-match name lookup over payload.Processors. As
// in findConnector, no Levenshtein/fuzzy suggestion is computed: a first
// registration is the one place typosquatting has no cryptographic backstop,
// so this pipeline must never nudge a typo toward an existing name.
func findProcessor(payload index.Payload, name string) (*index.Processor, error) {
	for i := range payload.Processors {
		if payload.Processors[i].Name == name {
			return &payload.Processors[i], nil
		}
	}
	e := conduiterr.New(CodeProcessorNotFound, fmt.Sprintf(
		"processor %q not found in the registry index", name))
	e.Suggestion = "check the exact spelling — this is an exact-match lookup with no fuzzy suggestion, by design; if the hosted index does not yet carry processors, use the interim offline (--index-file/--bundle) install path"
	return nil, e
}

func resolveProcessorExact(proc *index.Processor, opts ResolveOptions) (*ResolvedProcessorVersion, error) {
	wantV, err := NormalizeVersion(opts.Version)
	if err != nil {
		return nil, conduiterr.Wrap(CodeVersionNotFound,
			fmt.Sprintf("processor %q: %q is not a valid version", proc.Name, opts.Version), err)
	}

	for i := range proc.Versions {
		v := proc.Versions[i]
		gotV, err := NormalizeVersion(v.Version)
		if err != nil {
			continue // a malformed index entry; skip defensively rather than fail the whole resolution
		}
		if !gotV.Equal(wantV) {
			continue
		}

		if v.Yanked != nil {
			e := conduiterr.New(index.CodeVersionYanked, fmt.Sprintf(
				"processor %q version %s was yanked: %s", proc.Name, v.Version, v.Yanked.Reason))
			e.Suggestion = "install a different version, or omit @version to auto-select the newest non-yanked, compatible one"
			return nil, e
		}
		if err := checkProcessorCompatible(proc.Name, v, opts); err != nil {
			return nil, err
		}
		return &ResolvedProcessorVersion{Processor: *proc, Version: v}, nil
	}

	return nil, conduiterr.New(CodeVersionNotFound, fmt.Sprintf(
		"processor %q has no version %q in the registry index", proc.Name, opts.Version))
}

func resolveProcessorNewestCompatible(proc *index.Processor, opts ResolveOptions) (*ResolvedProcessorVersion, error) {
	versions := append([]index.ProcessorVersion(nil), proc.Versions...)
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
		if err := checkProcessorCompatible(proc.Name, v, opts); err != nil {
			continue // not compatible with the running build; try the next-newest
		}
		return &ResolvedProcessorVersion{Processor: *proc, Version: v}, nil
	}

	e := conduiterr.New(CodeIncompatibleVersion, fmt.Sprintf(
		"no version of %q is compatible with the running Conduit (conduit %s, protocol %s)",
		proc.Name, opts.RunningConduitVersion, opts.RunningProtocolVersion))
	e.Suggestion = "upgrade Conduit, or pin an older @version explicitly if you know one is compatible"
	return nil, e
}

// checkProcessorCompatible refuses a candidate version whose MinConduitVersion
// or MinProtocolVersion exceeds the running build's own versions. It reuses
// checkMinVersion (resolve.go) verbatim — the compatibility algebra is
// identical to connectors; only the error wording/type differs.
func checkProcessorCompatible(name string, v index.ProcessorVersion, opts ResolveOptions) error {
	if err := checkMinVersion("minConduitVersion", v.MinConduitVersion, opts.RunningConduitVersion); err != nil {
		return newProcessorIncompatibleError(name, v, opts)
	}
	if err := checkMinVersion("minProtocolVersion", v.MinProtocolVersion, opts.RunningProtocolVersion); err != nil {
		return newProcessorIncompatibleError(name, v, opts)
	}
	return nil
}

// newProcessorIncompatibleError is newIncompatibleError's processor twin:
// CodeIncompatibleVersion with the required-vs-running facts and a concrete fix
// suggestion ("errors are API").
func newProcessorIncompatibleError(name string, v index.ProcessorVersion, opts ResolveOptions) error {
	e := conduiterr.New(CodeIncompatibleVersion, fmt.Sprintf(
		"processor %q version %s requires Conduit >= %s and protocol >= %s; running Conduit %s, protocol %s",
		name, v.Version, v.MinConduitVersion, v.MinProtocolVersion, opts.RunningConduitVersion, opts.RunningProtocolVersion))
	e.Suggestion = fmt.Sprintf(
		"upgrade Conduit to >= %s, or install an older %s version compatible with your running Conduit (omit @version to auto-select one)",
		v.MinConduitVersion, name)
	return e
}
