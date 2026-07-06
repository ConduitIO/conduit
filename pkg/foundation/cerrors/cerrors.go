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

import (
	"errors" //nolint:depguard // the std. errors package is allowed only in this package
	"fmt"
	"reflect"
	"runtime"

	"golang.org/x/xerrors" //nolint:depguard // the xerrors package is allowed only in this package
)

var (
	// xerrors is used, since it provides the stack traces too
	New    = xerrors.New    //nolint:forbidigo // xerrors.New is allowed here, but not anywhere else
	Errorf = xerrors.Errorf //nolint:forbidigo // xerrors.Errorf is allowed here, but not anywhere else
	Is     = errors.Is
	As     = errors.As
	Unwrap = errors.Unwrap
	Join   = errors.Join
)

// stackError is a cerrors-native equivalent of xerrors' unexported
// errorString, used by NewWithStackDepth. It exists only because xerrors.New
// hardcodes its caller depth (always attributes the frame to its immediate
// caller) and exposes no way to skip additional frames. hasStackTrace and
// getRuntimeFrame recognize it structurally (a "frame" field of type
// xerrors.Frame), the same way they walk xerrors' own error type.
type stackError struct {
	s     string
	frame xerrors.Frame
}

func (e *stackError) Error() string { return e.s }

func (e *stackError) Format(s fmt.State, v rune) { xerrors.FormatError(e, s, v) }

func (e *stackError) FormatError(p xerrors.Printer) (next error) {
	p.Print(e.s)
	e.frame.Format(p)
	return nil
}

// NewWithStackDepth is like New, but lets the caller skip additional stack
// frames when attributing the captured frame. New (equivalent to skip=0)
// always attributes the frame to its own immediate caller; a function that
// itself wraps New (e.g. conduiterr.New) needs skip=1 so the frame lands on
// *its* caller instead of on the wrapper. skip counts wrapper layers between
// the real call site and this function, analogous to runtime.Caller's skip
// but relative to New's existing (fixed) depth.
func NewWithStackDepth(skip int, msg string) error {
	return &stackError{s: msg, frame: xerrors.Caller(1 + skip)}
}

type Frame struct {
	Func string `json:"func,omitempty"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
}

func GetStackTrace(err error) interface{} {
	defer func() { recover() }() //nolint:errcheck // GetStackTrace is used for logging, so we don't want logging panics to crash the whole service

	var frames []Frame
	for w := err; w != nil; w = errors.Unwrap(w) {
		if hasStackTrace(w) {
			frames = append(frames, getRuntimeFrame(w))
		}
	}

	return frames
}

// LogOrReplace is a utility meant to be called in deferred functions that can
// produce an error. In that case we have two options:
//
//  1. Group errors together (e.g. with the multierror package).
//  2. Use LogOrReplace to either log the error or return it.
//
// The second option is preferable when the original error returning from the
// function is more important than the deferred error. LogOrReplace will return
// the original error if it is not nil, otherwise it will return the new error.
// If both errors are not nil, then the log function will be called, that can be
// used to log the new error.
//
// Example how it's supposed to be used:
//
//	func() (err error) {
//	  defer func() {
//	    cleanupErr := cleanup()
//	    err = cerrors.LogOrReplace(err, cleanupErr, func() {
//	      fmt.Printf("cleanup error: %v", cleanupErr)
//	    })
//	  }
//	  // execute logic that can produce an error
//	  return err
//	}
func LogOrReplace(oldErr, newErr error, log func()) error {
	if oldErr == nil {
		return newErr
	}
	if newErr != nil {
		log()
	}
	return oldErr
}

// ForEach is a utility function that can be used to iterate over all errors in
// a multierror created using Join. It will call the provided function for each
// error in the chain.
func ForEach(err error, fn func(error)) {
	multiErr, ok := err.(interface{ Unwrap() []error })
	if !ok {
		fn(err)
		return
	}
	for _, w := range multiErr.Unwrap() {
		ForEach(w, fn)
	}
}

// frameType is the reflect.Type of xerrors.Frame, used to recognize any
// error carrying one structurally, regardless of which package defined the
// concrete error type. This lets hasStackTrace/getRuntimeFrame walk both
// xerrors' own error type and cerrors' stackError (see NewWithStackDepth)
// without hardcoding "golang.org/x/xerrors" as the only allowed origin.
var frameType = reflect.TypeOf(xerrors.Frame{})

func hasStackTrace(err error) bool {
	errT := reflect.TypeOf(err)
	if errT == nil || errT.Kind() != reflect.Pointer {
		return false
	}
	f, ok := errT.Elem().FieldByName("frame")
	return ok && f.Type == frameType
}

func getRuntimeFrame(err error) Frame {
	frame := reflect.ValueOf(err).Elem().FieldByName("frame") // type Frame struct{ frames [3]uintptr }
	framesField := frame.FieldByName("frames")
	pc := make([]uintptr, framesField.Len())
	for i := 0; i < framesField.Len(); i++ {
		pc[i] = uintptr(framesField.Index(i).Uint())
	}

	// The following lines of code mimic xerrors' printing of an error in extended format.
	frames := runtime.CallersFrames(pc)
	if _, ok := frames.Next(); !ok {
		// Even though this is a very strange situation, we don't want to panic,
		// since this is used only in the context of logging.
		return Frame{}
	}
	fr, ok := frames.Next()
	if !ok {
		// Even though this is a very strange situation, we don't want to panic,
		// since this is used only in the context of logging.
		return Frame{}
	}
	return Frame{
		Func: fr.Function,
		File: fr.File,
		Line: fr.Line,
	}
}
