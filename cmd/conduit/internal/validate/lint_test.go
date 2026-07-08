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

package validate

import (
	"context"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

// AC-1: lint surfaces a deprecated-field warning as an advisory
// SeverityWarning finding located by line and column; warnings alone do not
// fail the run (report.OK() stays true).
func TestRunWithOptions_Warnings_SurfacesLocatedWarning(t *testing.T) {
	is := is.New(t)

	report, err := RunWithOptions(context.Background(), "testdata/warning.yaml", Options{Warnings: true})
	is.NoErr(err)
	is.True(report.OK())                 // warnings never fail without --strict
	is.Equal(report.Summary.Errors, 0)   // no errors
	is.Equal(report.Summary.Warnings, 1) // exactly one warning
	is.Equal(len(report.Files), 1)       //
	is.Equal(len(report.Files[0].Findings), 1)

	w := report.Files[0].Findings[0]
	is.Equal(w.Severity, SeverityWarning)
	is.Equal(w.Code, CodeLintWarning)
	is.True(w.Line > 0)   // located by line...
	is.True(w.Column > 0) // ...and column
	is.True(w.Message != "")
}

// validate (Options{}) is errors-only: the same file lint flags a warning on
// produces zero findings under validate, proving the warning channel is opt-in.
func TestRun_ValidateMode_IgnoresWarnings(t *testing.T) {
	is := is.New(t)

	report, err := Run(context.Background(), "testdata/warning.yaml")
	is.NoErr(err)
	is.True(report.OK())
	is.Equal(report.Summary.Warnings, 0)
	is.Equal(report.Summary.Errors, 0)
	is.Equal(len(report.Files[0].Findings), 0)
}

// AC-3: a file with both a validation error and an advisory warning surfaces
// both; the error dominates the verdict (report.OK() is false).
func TestRunWithOptions_ErrorAndWarning_Both(t *testing.T) {
	is := is.New(t)

	report, err := RunWithOptions(context.Background(), "testdata/warning-and-error.yaml", Options{Warnings: true})
	is.NoErr(err)
	is.True(!report.OK()) // the error fails the run
	is.Equal(report.Summary.Errors, 1)
	is.Equal(report.Summary.Warnings, 1)

	var sawErr, sawWarn bool
	for _, f := range report.Files[0].Findings {
		switch f.Severity {
		case SeverityError:
			sawErr = true
		case SeverityWarning:
			sawWarn = true
		}
	}
	is.True(sawErr)
	is.True(sawWarn)
}

// AC-2: ExitErrorStrict is the exit-code side of lint's --strict. Errors always
// yield an exit error; warnings do so only under --strict.
func TestExitErrorStrict(t *testing.T) {
	warnOnly := []FileReport{{Findings: []Finding{{Severity: SeverityWarning, Code: CodeLintWarning}}}}
	errOnly := []FileReport{{Findings: []Finding{{Severity: SeverityError, Code: "config.field_required"}}}}
	clean := []FileReport{{Findings: []Finding{}}}

	cases := []struct {
		name    string
		files   []FileReport
		strict  bool
		wantErr bool
		// wantBucket is the grpc-derived bucket the synthesized error must fall
		// into (2 = Validation) when wantErr is true; 0 means "don't check".
		wantValidation bool
	}{
		{"warnings, not strict → no exit error", warnOnly, false, false, false},
		{"warnings, strict → validation error", warnOnly, true, true, true},
		{"errors, not strict → exit error", errOnly, false, true, false},
		{"errors, strict → exit error", errOnly, true, true, false},
		{"clean, strict → no exit error", clean, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err, ok := ExitErrorStrict(tc.files, tc.strict)
			is.Equal(ok, tc.wantErr)
			if tc.wantErr {
				is.True(err != nil)
			}
			if tc.wantValidation {
				ce, got := conduiterr.Get(err)
				is.True(got)
				is.Equal(bucketRank(ce.Code), 2) // Validation
			}
		})
	}
}
