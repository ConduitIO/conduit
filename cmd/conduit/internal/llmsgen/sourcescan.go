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

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// registeredCode is one `[conduiterr.]Register("reason", codes.X)`
// call-site found by scanRegisteredCodes: the reason literal plus whatever
// doc comment (if any) immediately precedes its declaration.
type registeredCode struct {
	Reason      string
	Description string // from the nearest preceding doc comment, or ""
	File        string // path relative to repo root, for error messages
}

// scanRegisteredCodes statically scans every non-test .go file under
// repoRoot for a call to conduiterr.Register (qualified) or, within
// package conduiterr itself, the bare Register (its own var initializers
// call it unqualified). It returns one registeredCode per call-site.
//
// This is the independent, source-of-truth side of D5/AC-12: rather than
// trusting that the generator's own import list (allcodes) is complete, a
// separate mechanism — reading the actual .go files, not the import graph
// — re-derives the "true" set of registered reasons. allcodes.Codes() is
// then compared against this set in TestAllCodesComplete; if they disagree,
// either allcodes is missing a package (under-reporting) or this scan has
// a bug (over-reporting) — either way the drift is caught as a test
// failure, not shipped silently in llms.txt.
//
// A registration whose reason isn't a plain string literal (e.g. built
// from a variable or expression) cannot be resolved statically; every
// Register call in the engine today uses a literal, so scanRegisteredCodes
// treats a non-literal reason as an error rather than silently skipping it
// — a future non-literal reason should fail loudly here, not produce an
// undercount nobody notices.
func scanRegisteredCodes(repoRoot string) ([]registeredCode, error) {
	var out []registeredCode

	fset := token.NewFileSet()
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			// Skip build/test-fixture dirs, hidden dirs, and nested module
			// checkouts so a local run scans the same set a clean CI checkout
			// does. Hidden dirs matter because .claude/worktrees holds gitignored
			// full-repo copies (absent in CI) that would otherwise double-count
			// Register call-sites — a green-CI/red-local split, the inverse of the
			// drift this generator prevents. A subdir with its own go.mod is a
			// nested module, not part of this one.
			if name == "node_modules" || name == "testdata" {
				return filepath.SkipDir
			}
			if path != repoRoot && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if path != repoRoot {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			rel = path
		}

		found, scanErr := scanFileForRegisterCalls(fset, path, rel)
		if scanErr != nil {
			return scanErr
		}
		out = append(out, found...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan repo for Register call-sites: %w", err)
	}
	return out, nil
}

// scanFileForRegisterCalls parses one Go source file and extracts every
// Register call-site it contains, per scanRegisteredCodes's contract.
func scanFileForRegisterCalls(fset *token.FileSet, path, rel string) ([]registeredCode, error) {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}

	// bare Register(...) only resolves to conduiterr.Register within
	// package conduiterr itself (its own var initializers call it
	// unqualified); everywhere else it must be qualified, and the alias is
	// whatever this file imported the package as (default "conduiterr").
	selfPackage := f.Name.Name == "conduiterr"
	conduiterrAlias := importAlias(f, "github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr")

	cmap := ast.NewCommentMap(fset, f, f.Comments)

	// A doc comment on a standalone `var X = Register(...)` attaches to the
	// GenDecl, not to the ValueSpec inside it — only specs in a `var ( ... )`
	// block carry their own. Without a fallback the description silently
	// degrades to the humanized reason, which reads like a placeholder in the
	// shipped reference.
	//
	// The fallback is deliberately limited to single-spec declarations, which
	// is exactly the case go/ast conflates. A `var ( ... )` block's comment
	// describes the GROUP, so inheriting it would stamp the same paragraph
	// onto every uncommented code in the block — worse than no description,
	// because it reads as if it were written about that specific code.
	declFor := make(map[*ast.ValueSpec]*ast.GenDecl)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || len(gd.Specs) != 1 {
			continue
		}
		if vs, ok := gd.Specs[0].(*ast.ValueSpec); ok {
			declFor[vs] = gd
		}
	}

	var out []registeredCode
	var walkErr error

	ast.Inspect(f, func(n ast.Node) bool {
		if walkErr != nil {
			return false
		}
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, val := range spec.Values {
			call, ok := val.(*ast.CallExpr)
			if !ok || !isRegisterCall(call, selfPackage, conduiterrAlias) {
				continue
			}
			reason, litErr := registerCallReason(call, fset, rel)
			if litErr != nil {
				walkErr = litErr
				return false
			}
			out = append(out, registeredCode{
				Reason:      reason,
				Description: docCommentFor(cmap, spec, declFor[spec]),
				File:        rel,
			})
		}
		return true
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// importAlias returns the local identifier path is imported under in f, or
// its default package name (the final path segment) if f imports it
// without an alias, or "" if f does not import it at all.
func importAlias(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil || importPath != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		segments := strings.Split(importPath, "/")
		return segments[len(segments)-1]
	}
	return ""
}

// isRegisterCall reports whether call is a Register(...) invocation this
// scanner should treat as a code registration: either bare Register(...)
// in package conduiterr itself, or <alias>.Register(...) where alias is
// the file's import name for the conduiterr package.
func isRegisterCall(call *ast.CallExpr, selfPackage bool, conduiterrAlias string) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return selfPackage && fn.Name == "Register"
	case *ast.SelectorExpr:
		if fn.Sel.Name != "Register" {
			return false
		}
		x, ok := fn.X.(*ast.Ident)
		return ok && conduiterrAlias != "" && x.Name == conduiterrAlias
	default:
		return false
	}
}

// registerCallReason extracts the reason string literal from a Register
// call's first argument.
func registerCallReason(call *ast.CallExpr, fset *token.FileSet, rel string) (string, error) {
	if len(call.Args) == 0 {
		return "", fmt.Errorf("%s:%d: Register call has no arguments", rel, fset.Position(call.Pos()).Line)
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", fmt.Errorf(
			"%s:%d: Register's reason argument is not a string literal (got %T) — "+
				"scanRegisteredCodes only supports literal reasons",
			rel, fset.Position(call.Pos()).Line, call.Args[0],
		)
	}
	reason, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", fmt.Errorf("%s:%d: unquote reason literal: %w", rel, fset.Position(call.Pos()).Line, err)
	}
	return reason, nil
}

// docCommentFor returns the text of the doc comment attached to spec by
// go/ast's own comment-association heuristic (ast.NewCommentMap), limited
// to comment groups that end before spec starts (its leading doc comment,
// as opposed to a trailing line comment on the same line).
//
// decl is the declaration spec belongs to, and may be nil. It is consulted
// only when spec itself carries no doc comment: go/ast attaches the comment
// of a standalone `var X = ...` to the declaration rather than to the single
// spec inside it, so without this fallback every code declared outside a
// `var ( ... )` block would render with no description at all.
func docCommentFor(cmap ast.CommentMap, spec *ast.ValueSpec, decl *ast.GenDecl) string {
	if text := leadingDoc(cmap[spec], spec.Pos()); text != "" {
		return text
	}
	if decl == nil {
		return ""
	}
	return leadingDoc(cmap[decl], decl.Pos())
}

// leadingDoc returns the NEAREST comment group in groups that ends before pos
// — the doc comment, never a trailing line comment on the same line.
//
// Nearest, not all of them: a file can carry a detached note above a
// declaration (a rationale paragraph, a TODO), and go/ast associates every
// such group with the following node. Concatenating them puts repo-process
// prose into the shipped, agent-facing error reference.
func leadingDoc(groups []*ast.CommentGroup, pos token.Pos) string {
	var nearest *ast.CommentGroup
	for _, g := range groups {
		if g.End() >= pos {
			continue // trailing comment, not a leading doc comment
		}
		if nearest == nil || g.End() > nearest.End() {
			nearest = g
		}
	}
	if nearest == nil {
		return ""
	}
	return strings.TrimSpace(nearest.Text())
}
