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

// Insert adds a path with a value to the tree
func (t *YAMLTree) Insert(path, value, comment string) {
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

		var valueNode *yaml.Node
		found := false

		// Search for existing key
		for j := 0; j < len(current.Content); j += 2 {
			if current.Content[j].Value == part {
				valueNode = current.Content[j+1]
				found = true
				break
			}
		}

		if !found {
			if isLast {
				valueNode = &yaml.Node{
					Kind:  yaml.ScalarNode,
					Value: value,
				}
			} else {
				valueNode = &yaml.Node{
					Kind: yaml.MappingNode,
				}
			}
			current.Content = append(current.Content, keyNode, valueNode)
		}

		current = valueNode
	}
}
