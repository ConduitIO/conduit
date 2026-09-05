// Copyright © 2024 Meroxa, Inc.
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

package internal

import (
	"strings"

	"github.com/conduitio/yaml/v3"
)

// YAMLTree represents a YAML document.
// It makes it possible to insert value nodes with comments.
type YAMLTree struct {
	Root *yaml.Node
}

func NewYAMLTree() *YAMLTree {
	return &YAMLTree{
		Root: &yaml.Node{
			Kind: yaml.MappingNode,
		},
	}
}

// Insert adds a path with a scalar value to the tree.
func (t *YAMLTree) Insert(path, value, comment string) {
	t.insertNode(path, &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: value,
	}, comment)
}

// InsertSeq adds a path holding a YAML sequence of scalars.
//
// A sequence must be inserted as a real SequenceNode, never as a scalar: a
// Go slice formatted through fmt (%v) yields "[]" or "[a b]", which the
// encoder then emits as the *quoted string* '[]' — a value the config loader
// rejects. That produced a conduit.yaml `conduit run` refused to start with,
// so slices go through this method and never through Insert.
func (t *YAMLTree) InsertSeq(path string, values []string, comment string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	if len(values) == 0 {
		// Flow style so an empty sequence renders as `key: []` rather than a
		// dangling key with a nil value.
		seq.Style = yaml.FlowStyle
	}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: v,
		})
	}
	t.insertNode(path, seq, comment)
}

// insertNode walks/creates the mapping path and attaches valueNode at the leaf.
func (t *YAMLTree) insertNode(path string, valueNode *yaml.Node, comment string) {
	parts := strings.Split(path, ".")
	current := t.Root

	for i, part := range parts {
		isLast := i == len(parts)-1
		keyNode := &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: part,
		}

		if comment != "" && isLast {
			keyNode.HeadComment = "# " + comment
		}

		var node *yaml.Node
		found := false

		// Search for existing key
		for j := 0; j < len(current.Content); j += 2 {
			if current.Content[j].Value == part {
				node = current.Content[j+1]
				found = true
				break
			}
		}

		if !found {
			if isLast {
				node = valueNode
			} else {
				node = &yaml.Node{
					Kind: yaml.MappingNode,
				}
			}
			current.Content = append(current.Content, keyNode, node)
		}

		current = node
	}
}
