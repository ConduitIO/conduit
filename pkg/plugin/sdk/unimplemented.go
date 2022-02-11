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

package sdk

import "context"

// UnimplementedDestination should be embedded to have forward compatible implementations.
type UnimplementedDestination struct{}

func (UnimplementedDestination) Configure(context.Context, map[string]string) error {
	return ErrUnimplemented
}
func (UnimplementedDestination) Open(context.Context) error {
	return ErrUnimplemented
}
func (UnimplementedDestination) Write(context.Context, Record) error {
	return ErrUnimplemented
}
func (UnimplementedDestination) WriteAsync(context.Context, Record, AckFunc) error {
	return ErrUnimplemented
}
func (UnimplementedDestination) Flush(context.Context) error {
	return ErrUnimplemented
}
func (UnimplementedDestination) Teardown(context.Context) error {
	return ErrUnimplemented
}
func (UnimplementedDestination) mustEmbedUnimplementedDestination() {}

// UnimplementedSource should be embedded to have forward compatible implementations.
type UnimplementedSource struct{}

func (UnimplementedSource) Configure(context.Context, map[string]string) error {
	return ErrUnimplemented
}
func (UnimplementedSource) Open(context.Context, Position) error {
	return ErrUnimplemented
}
func (UnimplementedSource) Read(context.Context) (Record, error) {
	return Record{}, ErrUnimplemented
}
func (UnimplementedSource) Ack(context.Context, Position) error {
	return ErrUnimplemented
}
func (UnimplementedSource) Teardown(context.Context) error {
	return ErrUnimplemented
}
func (UnimplementedSource) mustEmbedUnimplementedSource() {}
