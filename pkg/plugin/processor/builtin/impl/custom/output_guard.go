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

package custom

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

const (
	// maxOutputDepth bounds how deeply a script's output may nest. It matches
	// encoding/json's decoder limit. Record.Clone and the encoders recurse
	// once per level, so an unbounded depth overflows the goroutine stack
	// (fatal, not recoverable) at a few million levels.
	maxOutputDepth = 10_000
	// maxOutputEntries bounds the expanded size of the output: map entries
	// plus slice elements, counting a shared value once per parent that
	// references it, because Record.Clone copies it once per parent. Without
	// it, a few dozen levels of `x = [x, x]` stay under the depth limit yet
	// clone to 2^n values.
	maxOutputEntries = 1 << 24
)

// checkOutput verifies that the data a script returned can be safely cloned
// and walked by recursive code. It returns a coded conduiterr error when the
// data holds a cycle (CodeJSCyclicOutput) or exceeds a fixed limit
// (CodeJSOutputLimitExceeded), and nil otherwise. root names the value in
// error paths (for example ".Payload.After").
//
// goja hands Go maps and slices to the script by reference, so a script can
// store a value inside itself. A cycle is not a panic when it is consumed:
// the next Record.Clone recurses until "fatal error: stack overflow", which
// no recover() can catch.
//
// A value reachable by two different paths (a DAG) is not a cycle and is
// accepted. The walk visits each distinct container once, so it is linear in
// the number of containers.
func checkOutput(root string, v any) error {
	w := &outputWalker{
		root:   root,
		onPath: map[containerID]struct{}{},
		done:   map[containerID]int{},
	}
	_, err := w.walk(v, 0)
	return err
}

// containerID identifies a map or slice by its backing storage. Length and
// type are part of the identity because two slices can share a backing array
// and still be different values (s and s[:1]).
type containerID struct {
	ptr uintptr
	len int
	typ reflect.Type
}

type outputWalker struct {
	// root names the checked value in error paths; segs are the steps from it
	// to the value being visited. The path string is only built on error, so
	// a deep or wide output does not allocate one string per node.
	root string
	segs []string
	// onPath holds the containers on the current descent. Meeting one again
	// means the value contains itself.
	onPath map[containerID]struct{}
	// done maps fully checked containers to their expanded entry count, so a
	// shared value is walked once however many parents reference it.
	done map[containerID]int
}

// path returns the location of the value being visited.
func (w *outputWalker) path() string { return w.root + strings.Join(w.segs, "") }

// walk checks v and everything below it, depth levels below the root. It
// returns the expanded entry count of v.
func (w *outputWalker) walk(v any, depth int) (int, error) {
	switch v.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16,
		uint32, uint64, float32, float64, []byte, opencdc.RawData, *opencdc.RawData:
		return 0, nil
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() { //nolint:exhaustive // only containers can hold other values
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return 0, nil
		}
		// Pointers to data (for example *opencdc.RawData from RawData()) are
		// leaves. A pointer to a container is followed with the same
		// identity rules; its target is what can close a cycle.
		return w.walkValue(rv.Elem(), depth)
	}
	return w.walkValue(rv, depth)
}

func (w *outputWalker) walkValue(rv reflect.Value, depth int) (int, error) {
	var n int
	switch rv.Kind() { //nolint:exhaustive // only containers can hold other values
	case reflect.Map:
		n = rv.Len()
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return 0, nil // byte data, no nesting
		}
		n = rv.Len()
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return 0, nil
		}
		return w.walkValue(rv.Elem(), depth)
	default:
		return 0, nil
	}
	if n == 0 {
		return 0, nil
	}

	var id containerID
	trackable := rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice
	if trackable {
		id = containerID{ptr: rv.Pointer(), len: n, typ: rv.Type()}
		if _, ok := w.onPath[id]; ok {
			return 0, cyclicErr(w.path())
		}
		if size, ok := w.done[id]; ok {
			return size, nil
		}
		w.onPath[id] = struct{}{}
	}

	if depth >= maxOutputDepth {
		return 0, limitErr(w.path(), "nests deeper than "+strconv.Itoa(maxOutputDepth)+" levels")
	}

	total := n
	add := func(child reflect.Value, seg string) error {
		w.segs = append(w.segs, seg)
		size, err := w.walk(child.Interface(), depth+1)
		if err != nil {
			return err
		}
		w.segs = w.segs[:len(w.segs)-1]
		total += size
		if total > maxOutputEntries {
			return limitErr(w.path(), "expands to more than "+strconv.Itoa(maxOutputEntries)+" entries once shared values are counted at each place they appear")
		}
		return nil
	}

	switch rv.Kind() { //nolint:exhaustive // checked above
	case reflect.Map:
		it := rv.MapRange()
		for it.Next() {
			if err := add(it.Value(), "."+keyString(it.Key())); err != nil {
				return 0, err
			}
		}
	default:
		for i := 0; i < n; i++ {
			if err := add(rv.Index(i), "["+strconv.Itoa(i)+"]"); err != nil {
				return 0, err
			}
		}
	}

	if trackable {
		delete(w.onPath, id)
		w.done[id] = total
	}
	return total, nil
}

func keyString(k reflect.Value) string {
	if k.Kind() == reflect.String {
		return k.String()
	}
	return "<key>"
}

// shortPath keeps error messages readable; a path at the depth limit has
// thousands of segments.
func shortPath(path string) string {
	const maxLen = 200
	if len(path) <= maxLen {
		return path
	}
	return path[:maxLen/2] + "..." + path[len(path)-maxLen/2:]
}

func cyclicErr(path string) error {
	e := conduiterr.New(CodeJSCyclicOutput,
		"the record returned by the JavaScript process() function contains itself: the value at "+
			shortPath(path)+" is also one of its own parents")
	e.Suggestion = "copy the value instead of assigning an object to a field inside itself, " +
		"for example `Object.assign({}, value)`; the record was not emitted"
	return e
}

func limitErr(path, what string) error {
	e := conduiterr.New(CodeJSOutputLimitExceeded,
		"the record returned by the JavaScript process() function is too large to handle safely: the value at "+
			shortPath(path)+" "+what)
	e.Suggestion = "flatten the structure, drop fields you do not need, or do not reference the same value " +
		"from many parents; the record was not emitted"
	return e
}
