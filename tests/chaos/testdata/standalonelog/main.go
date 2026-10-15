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

// Command standalonelog is the log connector served as a standalone plugin,
// built from this module's go.mod so it runs the connector SDK version that
// Conduit itself pins. standalone_batch_sigterm_test.go builds it to drive a
// real Conduit process against a real standalone destination that uses SDK
// batching (ConduitIO/conduit#3092).
package main

import (
	log "github.com/conduitio/conduit-connector-log"
	sdk "github.com/conduitio/conduit-connector-sdk"
)

func main() {
	sdk.Serve(log.Connector)
}
