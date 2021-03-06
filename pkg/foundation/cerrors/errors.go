// Copyright © 2022 Meroxa, Inc.
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

// Package cerrors contains functions related to error handling.
//
// The standard library's errors package is missing some functionality which we need,
// such as stack traces. To be certain that all errors created in Conduit are created
// with the additional information, usage of this package is mandatory.
//
// At present, the package acts as a "thin forwarding layer", where we "mix and match"
// functions from different packages.
package cerrors

var (
	// ErrNotImpl should be used when a functionality which is not yet implemented was called.
	ErrNotImpl = New("not impl")
	// ErrEmptyID should be used when an entity was requested, but the ID was not provided.
	ErrEmptyID = New("empty ID")
)
