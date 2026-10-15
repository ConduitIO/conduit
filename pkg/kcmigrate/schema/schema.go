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

// Package schema holds the JSON Schema of the Kafka Connect migration
// compatibility report (report.v1.schema.json), the public contract of
// kcmigrate.Report, and a validator for it. The schema is the authority: the
// Go types are checked against it, never the reverse.
//
// Only tests and the migration harness import this package, so the
// validator's dependency stays out of the conduit binary.
package schema

import (
	"bytes"
	_ "embed"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ReportV1ID is the $id of ReportV1.
const ReportV1ID = "https://conduit.io/schemas/kc-report/v1.json"

// ReportV1 is the JSON Schema of reportVersion 1.
//
//go:embed report.v1.schema.json
var ReportV1 []byte

var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(ReportV1))
	if err != nil {
		return nil, cerrors.Errorf("parse report schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(ReportV1ID, doc); err != nil {
		return nil, cerrors.Errorf("add report schema: %w", err)
	}
	s, err := c.Compile(ReportV1ID)
	if err != nil {
		return nil, cerrors.Errorf("compile report schema: %w", err)
	}
	return s, nil
})

// ValidateReport returns an error describing every violation if report (a
// report.json document) does not conform to ReportV1.
func ValidateReport(report []byte) error {
	s, err := compiled()
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(report))
	if err != nil {
		return cerrors.Errorf("parse report: %w", err)
	}
	if err := s.Validate(inst); err != nil {
		return cerrors.Errorf("report does not match %s: %w", ReportV1ID, err)
	}
	return nil
}
