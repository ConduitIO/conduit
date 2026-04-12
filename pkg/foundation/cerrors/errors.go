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

package cerrors

import (
	"fmt"
	"google.golang.org/grpc/codes"
)

// GRPCCodeError is an interface that can be implemented by errors to provide a gRPC status code.
type GRPCCodeError interface {
	Error() string
	GRPCCode() codes.Code
}

// grpcCodeError implements GRPCCodeError.
type grpcCodeError struct {
	msg  string
	code codes.Code
	err  error // wrapped error
}

func (e *grpcCodeError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %s", e.msg, e.err.Error())
	}
	return e.msg
}

func (e *grpcCodeError) GRPCCode() codes.Code {
	return e.code
}

func (e *grpcCodeError) Is(target error) bool {
	if e.err != nil {
		return Is(e.err, target)
	}
	// Direct comparison if no wrapped error
	_, ok := target.(*grpcCodeError)
	return ok && e.Error() == target.Error()
}

func (e *grpcCodeError) Unwrap() error {
	return e.err
}

// NewWithGRPCCode creates a new error with a specific gRPC status code.
func NewWithGRPCCode(code codes.Code, msg string) error {
	return &grpcCodeError{msg: msg, code: code}
}

// NewWithGRPCCodef creates a new error with a specific gRPC status code and formatted message.
func NewWithGRPCCodef(code codes.Code, format string, args ...any) error {
	return &grpcCodeError{msg: fmt.Sprintf(format, args...), code: code}
}

// WrapWithGRPCCode wraps an existing error with a specific gRPC status code.
func WrapWithGRPCCode(err error, code codes.Code, msg string) error {
	return &grpcCodeError{msg: msg, code: code, err: err}
}

// WrapWithGRPCCodef wraps an existing error with a specific gRPC status code and formatted message.
func WrapWithGRPCCodef(err error, code codes.Code, format string, args ...any) error {
	return &grpcCodeError{msg: fmt.Sprintf(format, args...), code: code, err: err}
}

// Is returns true if err is an instance of target.
func Is(err error, target error) bool {
	return is(err, target)
}

// Join returns an error that is the concatenation of all errors.
// If errs is empty, nil is returned.
func Join(errs ...error) error {
	return join(errs...)
}

// ErrNotImpl is a placeholder for something not implemented yet.
var ErrNotImpl = New("not implemented")
// ErrEmptyID is returned when an ID is expected but is empty.
var ErrEmptyID = New("ID is empty")
