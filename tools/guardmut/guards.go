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

// Command guardmut checks that the tests of a package notice when a data-path
// guard is removed.
//
// A guard is an `if` statement or a call statement marked with a comment:
//
//	// guard: slot-gap-refuse tests=^TestSlotGap pkgs=./other/pkg env=KEY=1
//	if gap {
//		return err
//	}
//
// guardmut removes each marked guard in turn (an `if` condition becomes
// `false && (cond)`, a call statement becomes an unevaluated closure), runs
// `go test` on the guard's package and requires the tests to fail. A mutant
// the tests do not kill SURVIVED: the guard can be deleted without any test
// noticing, which is the failure this tool exists to catch.
//
// Nothing is rewritten on disk: mutants are fed to the Go toolchain through
// `go test -overlay`.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Kind is what a guard marks.
type Kind string

const (
	// KindIf is an if statement; the mutant never takes the guarded branch.
	KindIf Kind = "if"
	// KindCall is a call statement; the mutant does not run the call.
	KindCall Kind = "call"
)

// Guard is one marked statement.
type Guard struct {
	Name  string   `json:"name"`
	File  string   `json:"file"` // slash-separated, relative to the root
	Line  int      `json:"line"`
	Kind  Kind     `json:"kind"`
	Tests string   `json:"tests,omitempty"` // -run regexp
	Pkgs  []string `json:"pkgs"`            // packages to test, ./relative
	Env   []string `json:"env,omitempty"`   // extra KEY=VALUE for go test

	start, end int    // byte range replaced in the file
	repl       string // replacement text
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*$`)

// Scan finds every guard marker in the non-test Go files under dirs
// (relative to root). Anything wrong with a marker is an error, never a
// silent skip: a marker that guards nothing would look like coverage.
func Scan(root string, dirs []string) ([]*Guard, error) {
	var guards []*Guard
	seen := map[string]string{}
	for _, d := range dirs {
		base := filepath.Join(root, d)
		err := filepath.WalkDir(base, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() {
				if n := de.Name(); n == "testdata" || n == "vendor" || strings.HasPrefix(n, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			gs, err := scanFile(p, filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			for _, g := range gs {
				if prev, ok := seen[g.Name]; ok {
					return fmt.Errorf("duplicate guard name %q at %s:%d and %s", g.Name, g.File, g.Line, prev)
				}
				seen[g.Name] = fmt.Sprintf("%s:%d", g.File, g.Line)
				guards = append(guards, g)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(guards, func(i, j int) bool { return guards[i].Name < guards[j].Name })
	return guards, nil
}

func scanFile(path, rel string) ([]*Guard, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var out []*Guard
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			text, ok := strings.CutPrefix(c.Text, "//")
			if !ok {
				continue
			}
			text, ok = strings.CutPrefix(strings.TrimSpace(text), "guard:")
			if !ok {
				continue
			}
			pos := fset.Position(c.Pos())
			where := fmt.Sprintf("%s:%d", rel, pos.Line)
			g, err := parseMarker(text, rel, pos.Line)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			standalone := len(bytes.TrimSpace(src[pos.Offset-(pos.Column-1):pos.Offset])) == 0
			target := pos.Line
			if standalone {
				target++
			}
			if err := bind(fset, f, src, g, target); err != nil {
				return nil, fmt.Errorf("%s: guard %q: %w", where, g.Name, err)
			}
			dir := filepath.ToSlash(filepath.Dir(rel))
			g.Pkgs = append([]string{"./" + dir}, g.Pkgs...)
			out = append(out, g)
		}
	}
	return out, nil
}

func parseMarker(text, rel string, line int) (*Guard, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil, fmt.Errorf("guard marker has no name")
	}
	g := &Guard{Name: fields[0], File: rel, Line: line}
	if !nameRE.MatchString(g.Name) {
		return nil, fmt.Errorf("guard name %q must match %s", g.Name, nameRE)
	}
	for _, f := range fields[1:] {
		switch {
		case strings.HasPrefix(f, "tests="):
			g.Tests = strings.TrimPrefix(f, "tests=")
			if _, err := regexp.Compile(g.Tests); err != nil {
				return nil, fmt.Errorf("tests= is not a regexp: %w", err)
			}
		case strings.HasPrefix(f, "env="):
			kv := strings.TrimPrefix(f, "env=")
			if !strings.Contains(kv, "=") {
				return nil, fmt.Errorf("env= entry %q must be KEY=VALUE", kv)
			}
			g.Env = append(g.Env, kv)
		case strings.HasPrefix(f, "pkgs="):
			for _, p := range strings.Split(strings.TrimPrefix(f, "pkgs="), ",") {
				if !strings.HasPrefix(p, "./") {
					return nil, fmt.Errorf("pkgs= entry %q must start with ./", p)
				}
				g.Pkgs = append(g.Pkgs, p)
			}
		default:
			return nil, fmt.Errorf("unknown option %q (want tests=<regexp>, pkgs=<./a,./b> or env=KEY=VALUE)", f)
		}
	}
	if g.Tests == "" {
		// Without tests= any failing test in the package "kills" the guard, so an unrelated
		// test or a package-level failure can hide a guard no test actually needs.
		return nil, fmt.Errorf("guard %q needs tests=<regexp> naming the test(s) that must fail when it is removed", g.Name)
	}
	return g, nil
}

// bind attaches the marker to the outermost statement that starts on target.
func bind(fset *token.FileSet, f *ast.File, src []byte, g *Guard, target int) error {
	var found ast.Stmt
	ast.Inspect(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		s, ok := n.(ast.Stmt)
		if !ok {
			return true
		}
		if _, isBlock := s.(*ast.BlockStmt); isBlock {
			return true
		}
		if fset.Position(s.Pos()).Line == target {
			found = s
			return false
		}
		return true
	})
	if found == nil {
		return fmt.Errorf("no statement starts on line %d (put the marker on the line above the statement, or trail it)", target)
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	switch s := found.(type) {
	case *ast.IfStmt:
		g.Kind = KindIf
		g.start, g.end = off(s.Cond.Pos()), off(s.Cond.End())
		g.repl = "false && (" + string(src[g.start:g.end]) + ")"
	case *ast.ExprStmt:
		if _, ok := s.X.(*ast.CallExpr); !ok {
			return fmt.Errorf("only an if statement or a call statement can be a guard, found %T", s.X)
		}
		g.Kind = KindCall
		g.start, g.end = off(s.Pos()), off(s.End())
		g.repl = "_ = func() { " + string(src[g.start:g.end]) + " }"
	default:
		return fmt.Errorf("only an if statement or a call statement can be a guard, found %T", found)
	}
	return nil
}

// Mutate returns the file's source with the guard removed.
func Mutate(root string, g *Guard) ([]byte, error) {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(g.File)))
	if err != nil {
		return nil, err
	}
	out := append([]byte{}, src[:g.start]...)
	out = append(out, g.repl...)
	return append(out, src[g.end:]...), nil
}
