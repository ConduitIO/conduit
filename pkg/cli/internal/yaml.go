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

// YAMLTree represents the complete YAML structure
type YAMLTree struct {
	Root *yaml.Node
}

// NewYAMLTree creates a new YAML tree
func NewYAMLTree() *YAMLTree {
	return &YAMLTree{
		Root: &yaml.Node{
			Kind: yaml.MappingNode,
		},
	}
}

// Insert adds a path with a value to the tree
func (t *YAMLTree) Insert(path, value, comment string) {
	parts := strings.Split(path, ".")
	current := t.Root

	// For each part of the path
	for i, part := range parts {
		// Create key node
		keyNode := &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: part,
		}

		// Find or create value node
		var valueNode *yaml.Node
		found := false

		// Look for existing key in current mapping
		for i := 0; i < len(current.Content); i += 2 {
			if current.Content[i].Value == part {
				valueNode = current.Content[i+1]
				found = true
				break
			}
		}

		// If not found, create new node
		if !found {
			// If this is the last part, create scalar value node
			if i == len(parts)-1 {
				valueNode = &yaml.Node{
					Kind:  yaml.ScalarNode,
					Value: value,
				}
				keyNode.HeadComment = comment
			} else {
				// Otherwise create mapping node for nesting
				valueNode = &yaml.Node{
					Kind: yaml.MappingNode,
				}
			}
			// Add key-value pair to current node's content
			current.Content = append(current.Content, keyNode, valueNode)
		}

		// Move to next level
		current = valueNode
	}
}
