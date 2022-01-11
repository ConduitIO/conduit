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

// Package assert declares dead-simple function testing helpers for Conduit that
// we can reasonably maintain ourselves.
package assert

import (
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// True fails the test if the condition evaluates to false.
func True(tb testing.TB, condition bool, msg string, v ...interface{}) {
	if !condition {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\033[31m%s:%d: "+msg+"\033[39m\n\n", append([]interface{}{filepath.Base(file), line}, v...)...)
		tb.FailNow()
	}
}

// Ok fails the test if err is not nil.
func Ok(tb testing.TB, err error) {
	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\033[31m%s:%d: unexpected error: %s\033[39m\n\n", filepath.Base(file), line, err.Error())
		tb.FailNow()
	}
}

// Equal fails the test if exp is not equal to act.
func Equal(tb testing.TB, exp, act interface{}) {
	if !reflect.DeepEqual(exp, act) {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\033[31m%s:%d:\n\n\texp: %#v\n\n\tgot: %#v\033[39m\n\n", filepath.Base(file), line, exp, act)
		tb.FailNow()
	}
}

// Nil fails if the value is not nil.
func Nil(tb testing.TB, act interface{}) {
	if act == nil {
		return
	}
	// need to check for typed nil values
	if !reflect.ValueOf(act).IsNil() {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\033[31m%s:%d: expected nil, got: %#v\033[39m\n\n", filepath.Base(file), line, act)
		tb.FailNow()
	}
}

// NotNil fails if the value is nil.
func NotNil(tb testing.TB, act interface{}) {
	// need to check for typed nil values
	if act == nil || reflect.ValueOf(act).IsNil() {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\033[31m%s:%d: expected not nil, got: %#v\033[39m\n\n", filepath.Base(file), line, act)
		tb.FailNow()
	}
}

// Error fails if the error is nil.
func Error(tb testing.TB, err error) {
	if err == nil {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\033[31m%s:%d: expected error, got nil\033[39m\n\n", filepath.Base(file), line)
		tb.FailNow()
	}
}
