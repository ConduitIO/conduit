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

// Package migrate is the migration harness for `conduit migrate
// kafka-connect` (#3010): a corpus of real Kafka Connect configurations under
// corpus/, and the tests that check it. The corpus format, the report
// contract the harness checks against and the runner rules are specified in
// docs/design-documents/20261014-migrate-kafka-connect-mapping-and-harness.md
// ("Harness corpus format"); README.md says how to add a case.
//
// The tests in this package run in the ordinary `go test ./...` and are
// hermetic: they need no docker, no network and no migrate command. They
// check that the corpus itself is sound (manifests, provenance, licenses,
// scrub-v1, certification and coverage declarations). The reader and the
// checks live in package harness so that the offline runner in
// pkg/kcmigrate can reuse them.
package migrate
