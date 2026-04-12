package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <new_version> <file1> [<file2>...]\n", os.Args[0])
		os.Exit(1)
	}

	newVersion := os.Args[1]
	filePaths := os.Args[2:]

	fmt.Printf("Updating connector versions to: %s\n", newVersion)

	for _, filePath := range filePaths {
		err := updateVersionInFile(filePath, newVersion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error updating version in %s: %v\n", filePath, err)
			os.Exit(1)
		}
	}
	fmt.Println("Successfully updated all connector versions.")
}

func updateVersionInFile(filePath, newVersion string) error {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("could not parse file %s: %w", filePath, err)
	}

	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		genDecl, ok := n.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			return true // Not a const declaration, continue
		}

		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			// Look for "Version" identifier
			for _, ident := range valueSpec.Names {
				if ident.Name == "Version" {
					if len(valueSpec.Values) > 0 {
						basicLit, ok := valueSpec.Values[0].(*ast.BasicLit)
						if ok && basicLit.Kind == token.STRING {
							// Update the string literal
							basicLit.Value = fmt.Sprintf("%q", newVersion)
							found = true
							return false // Found and updated, no need to continue inspecting
						}
					}
				}
			}
		}
		return true // Continue inspecting other nodes
	})

	if !found {
		return fmt.Errorf("const Version = \"...\" not found in %s", filePath)
	}

	var buf bytes.Buffer
	err = format.Node(&buf, fset, node)
	if err != nil {
		return fmt.Errorf("could not format AST for %s: %w", filePath, err)
	}

	err = os.WriteFile(filePath, buf.Bytes(), 0644)
	if err != nil {
		return fmt.Errorf("could not write updated file %s: %w", filePath, err)
	}

	fmt.Printf("  Updated %s to %s\n", filePath, newVersion)
	return nil
}
