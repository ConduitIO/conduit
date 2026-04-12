package main

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Printf("Usage: %s <file_path> <new_version>\n", os.Args[0])
		os.Exit(1)
	}

	filePath := os.Args[1]
	newVersion := os.Args[2]

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		log.Fatalf("Error parsing file %s: %v", filePath, err)
	}

	found := false
	ast.Walk(visitorFunc(func(n ast.Node) {
		if found {
			return // Stop walking once found and updated
		}

		genDecl, ok := n.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.VAR {
			return
		}

		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			if len(valueSpec.Names) > 0 && valueSpec.Names[0].Name == "version" {
				// Found 'var version' declaration
				lit := &ast.BasicLit{
					Kind:  token.STRING,
					Value: fmt.Sprintf("%q", newVersion), // Add quotes around the string literal
				}

				if len(valueSpec.Values) == 0 {
					// No initial value, add one
					valueSpec.Values = []ast.Expr{lit}
				} else if len(valueSpec.Values) == 1 {
					// Replace existing initial value
					valueSpec.Values[0] = lit
				} else {
					log.Fatalf("Unexpected number of values for 'var version' in %s", filePath)
				}
				found = true
				return
			}
		}
	}), node)

	if !found {
		log.Fatalf("Could not find 'var version' declaration in %s", filePath)
	}

	// Write the modified AST back to the file
	file, err := os.Create(filePath)
	if err != nil {
		log.Fatalf("Error creating file %s: %v", filePath, err)
	}
	defer file.Close()

	if err := format.Node(file, fset, node); err != nil {
		log.Fatalf("Error writing formatted code to file %s: %v", filePath, err)
	}

	log.Printf("Successfully updated version in %s to %s\n", filePath, newVersion)
}

type visitorFunc func(n ast.Node)

func (f visitorFunc) Visit(n ast.Node) ast.Visitor {
	f(n)
	return f
}
