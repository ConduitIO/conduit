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

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
)

const (
	targetFile = "pkg/conduit/version.go"
	varName    = "BuiltinConnectorVersion"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Printf("Usage: %s <new_version>\n", os.Args[0])
		os.Exit(1)
	}

	newVersion := os.Args[1]

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, targetFile, nil, parser.ParseComments)
	if err != nil {
		fmt.Printf("Error parsing %s: %v\n", targetFile, err)
		os.Exit(1)
	}

	var found bool
	ast.Inspect(node, func(n ast.Node) bool {
		if genDecl, ok := n.(*ast.GenDecl); ok && genDecl.Tok == token.VAR {
			for _, spec := range genDecl.Specs {
				if valueSpec, ok := spec.(*ast.ValueSpec); ok {
					for _, name := range valueSpec.Names {
						if name.Name == varName {
							if len(valueSpec.Values) > 0 {
								if basicLit, ok := valueSpec.Values[0].(*ast.BasicLit); ok && basicLit.Kind == token.STRING {
									basicLit.Value = strconv.Quote(newVersion)
									found = true
									return false // Stop inspecting this declaration
								}
							}
						}
					}
				}
			}
		}
		return true
	})

	if !found {
		fmt.Printf("Error: Variable %s not found in %s\n", varName, targetFile)
		os.Exit(1)
	}

	f, err := os.Create(targetFile)
	if err != nil {
		fmt.Printf("Error opening %s for writing: %v\n", targetFile, err)
		os.Exit(1)
	}
	defer f.Close()

	err = ast.Fprint(f, fset, node, nil)
	if err != nil {
		fmt.Printf("Error writing to %s: %v\n", targetFile, err)
		os.Exit(1)
	}

	fmt.Printf("Successfully updated %s to %s in %s\n", varName, newVersion, targetFile)
}
