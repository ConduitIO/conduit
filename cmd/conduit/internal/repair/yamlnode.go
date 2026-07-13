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

package repair

import (
	"bytes"
	"io"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/yaml/v3"
)

// yamlIndent is the indent width repair re-encodes a repaired file with. It
// matches the 2-space indent every hand-written and generated pipeline
// config fixture in this repository uses (see
// cmd/conduit/internal/validate/testdata and docs/design-documents' own
// examples) — using the encoder's own default (4) would reformat every
// line's indentation on any edit, which is not "unrelated formatting...
// preserved" (AC-12) even though the *comments* would still survive.
const yamlIndent = 2

// parseDoc decodes raw into a comment-preserving *yaml.Node document tree —
// the node-level editor this package's Apply mutates in place instead of a
// marshal-from-struct round trip (which would strip comments; see doc.go and
// the design doc's §10 schedule-risk note).
func parseDoc(raw []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, cerrors.Errorf("could not parse pipeline config as YAML: %w", err)
	}
	// repair edits a SINGLE YAML document (doc.go's v1 scope). The encoder in
	// marshalDoc re-emits only the first document, so on a multi-document
	// ('---'-separated) file --apply would silently drop every document after the
	// first — a config data-loss bug the pipeline-count guard in collect does not
	// catch (a first document with one pipeline followed by pipeline-less
	// documents passes it). Reject a multi-document file here rather than corrupt
	// it. io.EOF from the second Decode is the single-document case.
	if err := dec.Decode(new(yaml.Node)); err == nil {
		ce := conduiterr.New(conduiterr.CodeInvalidArgument,
			"repair requires a single YAML document per file; this file contains multiple '---'-separated documents")
		ce.Suggestion = "split the file so each '---' document is its own file — repair edits one document and would otherwise drop the rest"
		return nil, ce
	} else if !cerrors.Is(err, io.EOF) {
		return nil, cerrors.Errorf("could not parse pipeline config as YAML: %w", err)
	}
	return &doc, nil
}

// marshalDoc re-encodes doc, preserving comments and (via yamlIndent)
// matching this repository's 2-space indent convention.
func marshalDoc(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(yamlIndent)
	if err := enc.Encode(doc); err != nil {
		return nil, cerrors.Errorf("could not re-encode repaired pipeline config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, cerrors.Errorf("could not finalize repaired pipeline config: %w", err)
	}
	return buf.Bytes(), nil
}

// documentRoot returns the top-level mapping node of a parsed document (doc
// is what parseDoc returns: a DocumentNode wrapping exactly one root node),
// or nil if doc is not a well-formed single-document mapping.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

// singlePipelineNode returns the sole pipeline document's own node — root's
// "pipelines" sequence, required to have exactly one element — reporting ok
// = false for anything else (v1's map-keyed "pipelines" field, zero or more
// than one pipeline document, or a missing "pipelines" key entirely). This
// is the v1 scope boundary doc.go documents: repair only edits a single v2
// -shaped pipeline document.
func singlePipelineNode(doc *yaml.Node) (*yaml.Node, bool) {
	root := documentRoot(doc)
	if root == nil {
		return nil, false
	}
	_, pipelines, ok := mapLookup(root, "pipelines")
	if !ok || pipelines.Kind != yaml.SequenceNode || len(pipelines.Content) != 1 {
		return nil, false
	}
	return pipelines.Content[0], true
}

// mapLookup returns the index of key's KEY node in m.Content (mapping
// Content is a flat [k0,v0,k1,v1,...] list — index is always even) and its
// paired VALUE node. ok is false when m is not a MappingNode or key is not
// present.
func mapLookup(m *yaml.Node, key string) (keyIdx int, value *yaml.Node, ok bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return -1, nil, false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i, m.Content[i+1], true
		}
	}
	return -1, nil, false
}

// documentPath reconciles the two ConfigPath addressing conventions
// producers use (see classify's doc and doc.go) into one document-rooted
// segment slice navigateParent can walk from documentRoot:
//
//   - pkg/provisioning/config.Validate's fixes (v1 items #2-#4) are
//     pipeline-relative ("/status", "/processors/0/workers",
//     "/description") — repair.Collect only ever operates on a single
//     pipeline document (see singlePipelineNode), so these are prefixed
//     with "pipelines/0".
//   - the parser-warning rename fix (v1 item #1, code ==
//     config.CodeFieldRenamed) is already document-rooted (e.g.
//     "/pipelines/0/processors/0/type") — the decoder-hook path it was
//     built from (linter.go's newWarning) already starts at the document
//     root, so it is used as-is.
func documentPath(code, fixConfigPath string) []string {
	segs := pathSegments(fixConfigPath)
	if code == config.CodeFieldRenamed.Reason() {
		return segs
	}
	return append([]string{"pipelines", "0"}, segs...)
}

// navigateParent walks root along segments, returning the parent node that
// directly contains the FINAL segment (a MappingNode whose key is
// segments[len-1], or the SequenceNode index is not supported as a final
// segment — every v1 fix targets a scalar mapping value, never a whole
// sequence element) plus that final segment, for a caller to interpret via
// applySet/applyRemove/applyAdd/applyRename. ok is false if any
// intermediate segment does not resolve (a mapping key that doesn't exist,
// a sequence index out of range, or a scalar where a container was
// expected) — the caller's job is to turn that into
// CodeFixNoLongerApplies, never to guess.
func navigateParent(root *yaml.Node, segments []string) (parent *yaml.Node, last string, ok bool) {
	if root == nil || len(segments) == 0 {
		return nil, "", false
	}
	cur := root
	for _, seg := range segments[:len(segments)-1] {
		switch cur.Kind {
		case yaml.MappingNode:
			_, v, found := mapLookup(cur, seg)
			if !found {
				return nil, "", false
			}
			cur = v
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(cur.Content) {
				return nil, "", false
			}
			cur = cur.Content[idx]
		case yaml.DocumentNode, yaml.ScalarNode, yaml.AliasNode:
			// A scalar/alias/document node has no child to descend into by
			// key or index — the path expected more structure than the
			// tree has. Same "not applicable" outcome as an unresolved
			// mapping key or out-of-range sequence index, above.
			return nil, "", false
		default:
			return nil, "", false
		}
	}
	return cur, segments[len(segments)-1], true
}

// valueAt returns the CURRENT scalar value at segments (via navigateParent +
// a mapping lookup of the final segment), for rendering ProposedFix.Before —
// never for editing. ok is false if the path does not resolve to an
// existing scalar mapping value.
func valueAt(root *yaml.Node, segments []string) (string, bool) {
	parent, last, ok := navigateParent(root, segments)
	if !ok || parent.Kind != yaml.MappingNode {
		return "", false
	}
	_, v, found := mapLookup(parent, last)
	if !found {
		return "", false
	}
	return v.Value, true
}

// applySet overwrites an EXISTING mapping key's value scalar in place
// (mutating the value node's Value/Tag directly, never replacing the node
// itself), so any comment attached to that value node survives untouched.
// ok is false if parent is not a mapping or key is not present — the
// CodeFixNoLongerApplies case.
func applySet(parent *yaml.Node, key, value string, numeric bool) bool {
	_, v, ok := mapLookup(parent, key)
	if !ok {
		return false
	}
	setScalar(v, value, numeric)
	return true
}

// applyRemove splices a key/value pair out of a mapping's flat Content
// list. ok is false if parent is not a mapping or key is not present.
func applyRemove(parent *yaml.Node, key string) bool {
	idx, _, ok := mapLookup(parent, key)
	if !ok {
		return false
	}
	parent.Content = append(parent.Content[:idx], parent.Content[idx+2:]...)
	return true
}

// applyAdd appends a brand-new key/value scalar pair to a mapping. ok is
// false if parent is not a mapping or key already exists (an "add" fix is
// only ever appliable when the target key is genuinely absent — a key that
// already exists is a producer bug or a stale fix, not silently overwritten
// by an add).
func applyAdd(parent *yaml.Node, key, value string, numeric bool) bool {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return false
	}
	if _, _, exists := mapLookup(parent, key); exists {
		return false
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{}
	setScalar(v, value, numeric)
	parent.Content = append(parent.Content, k, v)
	return true
}

// applyRename renames an existing mapping key IN PLACE — mutating only the
// KEY node's Value/Tag, leaving the paired VALUE node (and any comments
// attached to either node) completely untouched. This is how the compound
// rename fix (config.CodeFieldRenamed, v1 item #1) is actually applied: it
// is functionally identical to the "remove old key, add new key with the
// old value" the design doc frames it as (§3.1 — staying within the frozen
// three-op {set,remove,add} model), but preserves field position and
// comment attachment exactly, which a remove+add pair on a mapping's flat
// Content list would not reliably do. ok is false if oldKey is not present.
func applyRename(parent *yaml.Node, oldKey, newKey string) bool {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return false
	}
	if _, _, exists := mapLookup(parent, newKey); exists {
		// The destination key is already present (e.g. a processor that —
		// unusually — sets both the deprecated "type" and the current
		// "plugin"). Renaming into it would create a duplicate mapping key,
		// which is not a valid edit. Report "not applicable" rather than
		// corrupt the file.
		return false
	}
	idx, _, ok := mapLookup(parent, oldKey)
	if !ok {
		return false
	}
	parent.Content[idx].Value = newKey
	parent.Content[idx].Tag = "!!str"
	return true
}

// setScalar mutates node in place into a plain scalar carrying value,
// tagged !!int when numeric (the only non-string v1 target — /workers) or
// !!str otherwise (SetString's convenience styling, e.g. literal-block style
// for a multi-line description). Any comments already on node are
// untouched, since only Kind/Tag/Value/Style are reassigned here.
func setScalar(node *yaml.Node, value string, numeric bool) {
	if numeric {
		node.Kind = yaml.ScalarNode
		node.Tag = "!!int"
		node.Value = value
		node.Style = 0
		node.Content = nil
		return
	}
	node.Content = nil
	node.SetString(value)
}

// isNumericPath reports whether configPath's target is the v1 fix set's one
// non-string field — a processor's /workers — so the applier writes an
// unquoted integer scalar rather than guessing from Fix.Value's own
// (always-string) type. This is the "applier interprets Value per the
// target node's expected type from the config schema, not by guessing"
// rule from the design doc §3 — schema knowledge the v1 producer set is
// small enough to hardcode rather than needing a real schema lookup.
func isNumericPath(configPath string) bool {
	return strings.HasSuffix(configPath, "/workers")
}
