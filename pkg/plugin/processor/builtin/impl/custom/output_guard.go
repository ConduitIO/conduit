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
//
// checkOutput applies the entry limit to this one value. To bound several
// values of one record together, use an outputChecker.
func checkOutput(root string, v any) error {
	return newOutputChecker().check(root, v)
}

// outputChecker checks the values of one record against a single shared entry
// budget: Record.Clone copies Key, Payload.Before and Payload.After, so the
// work and memory it needs is the sum of the three, and the limit is per
// record rather than per field. Create one per record; it is not safe for
// concurrent use.
type outputChecker struct {
	w    outputWalker
	used int
}

func newOutputChecker() *outputChecker {
	return &outputChecker{w: outputWalker{seen: map[containerID]checked{}}}
}

// check verifies v, naming it root in error paths, and charges its expanded
// entry count to the record's budget.
func (c *outputChecker) check(root string, v any) error {
	c.w.root = root
	c.w.segs = c.w.segs[:0]
	size, _, err := c.w.walk(v, 0)
	if err != nil {
		return err
	}
	c.used += size
	if c.used > maxOutputEntries {
		return limitErr(root, "pushes the record past "+strconv.Itoa(maxOutputEntries)+
			" entries: the key, the payload before and the payload after are counted together, "+
			"with a shared value counted at each place it appears")
	}
	return nil
}

// containerID identifies a map or slice by its backing storage. Length and
// type are part of the identity because two slices can share a backing array
// and still be different values (s and s[:1]).
type containerID struct {
	ptr uintptr
	len int
	typ reflect.Type
}

// Types of the containers goja's Export produces, for the fast path. They
// must equal what reflect.TypeOf would report for the same values, so a
// container gets the same identity on both paths.
var (
	typStringMap     = reflect.TypeOf(map[string]any(nil))
	typStructuredMap = reflect.TypeOf(opencdc.StructuredData(nil))
	typAnySlice      = reflect.TypeOf([]any(nil))
)

// inProgress marks a container on the current descent in outputWalker.seen
// (as its size). A finished container maps to its expanded entry count, which
// is at least 1 because empty containers are never recorded.
const inProgress = -1

// checked is what the walker remembers about a finished container.
type checked struct {
	// size is the expanded entry count of the container.
	size int
	// height is the number of container levels in its subtree, itself
	// included (a container holding only scalars has height 1). A shared
	// container is walked once, from wherever it was first seen; height lets
	// every later parent re-check the depth limit against its own depth.
	height int
}

// pathSeg is one step of an error path. It is kept as data and only
// formatted on error, so a deep or wide output allocates nothing per node.
type pathSeg struct {
	key   string
	index int
	isIdx bool
}

type outputWalker struct {
	// root names the checked value in error paths; segs are the steps from it
	// to the value being visited.
	root string
	segs []pathSeg
	// seen holds every container met so far. A container mapped to inProgress
	// is on the current descent: meeting it again means the value contains
	// itself. Any other value is the expanded entry count of a fully checked
	// container, so a shared value is walked once however many parents
	// reference it.
	seen map[containerID]checked
}

// path returns the location of the value being visited.
func (w *outputWalker) path() string {
	var sb strings.Builder
	sb.WriteString(w.root)
	for _, s := range w.segs {
		if s.isIdx {
			sb.WriteByte('[')
			sb.WriteString(strconv.Itoa(s.index))
			sb.WriteByte(']')
			continue
		}
		sb.WriteByte('.')
		sb.WriteString(s.key)
	}
	return sb.String()
}

// isLeaf reports whether v is a value that cannot hold other values, for the
// types goja's Export and opencdc produce. A false result only means the
// value needs a closer look (walk falls back to reflection for it).
func isLeaf(v any) bool {
	switch v.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16,
		uint32, uint64, float32, float64, []byte, opencdc.RawData, *opencdc.RawData:
		return true
	}
	return false
}

// walk checks v and everything below it, depth levels below the root. It
// returns the expanded entry count of v and its height (see checked).
func (w *outputWalker) walk(v any, depth int) (size, height int, err error) {
	if isLeaf(v) {
		return 0, 0, nil
	}
	return w.walkContainer(v, depth)
}

// walkContainer is walk for a value already known not to be a leaf.
//
// The type switch covers the shapes goja's Export produces, which is nearly
// every record; it avoids reflect.Value.Interface, which allocates for each
// child. Anything else (typed maps and slices from Go-side code, pointers to
// containers) goes through reflection with the same identity and limit rules.
func (w *outputWalker) walkContainer(v any, depth int) (size, height int, err error) {
	switch t := v.(type) {
	case map[string]any:
		return w.walkStringMap(t, typStringMap, depth)
	case opencdc.StructuredData:
		return w.walkStringMap(t, typStructuredMap, depth)
	case []any:
		return w.walkAnySlice(t, depth)
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() { //nolint:exhaustive // only containers can hold other values
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return 0, 0, nil
		}
		// Pointers to data (for example *opencdc.RawData from RawData()) are
		// leaves. A pointer to a container is followed with the same
		// identity rules; its target is what can close a cycle.
		return w.walkValue(rv.Elem(), depth)
	}
	return w.walkValue(rv, depth)
}

// enter registers a non-empty container before its children are walked. If
// it returns cached, the container was already fully checked and c holds what
// was learned. Invariant 3: a cycle or runaway depth is rejected here, before
// anything recurses into it. A cached container is re-checked against the
// depth it appears at now, not the depth where it was first seen: the same
// container reached late in a long chain is deeper than its first sighting.
func (w *outputWalker) enter(id containerID, depth int) (c checked, cached bool, err error) {
	if c, ok := w.seen[id]; ok {
		if c.size == inProgress {
			return checked{}, false, cyclicErr(w.path())
		}
		if depth+c.height > maxOutputDepth {
			return checked{}, false, w.tooDeep()
		}
		return c, true, nil
	}
	w.seen[id] = checked{size: inProgress}
	if depth >= maxOutputDepth {
		return checked{}, false, w.tooDeep()
	}
	return checked{}, false, nil
}

// leave records a fully checked container.
func (w *outputWalker) leave(id containerID, c checked) { w.seen[id] = c }

func (w *outputWalker) tooDeep() error {
	return limitErr(w.path(), "nests deeper than "+strconv.Itoa(maxOutputDepth)+" levels")
}

func (w *outputWalker) tooBig() error {
	return limitErr(w.path(), "expands to more than "+strconv.Itoa(maxOutputEntries)+" entries once shared values are counted at each place they appear")
}

// descend walks child one level below the current container. Leaves return
// immediately without touching the path.
func (w *outputWalker) descend(child any, seg pathSeg, depth int) (size, height int, err error) {
	if isLeaf(child) {
		return 0, 0, nil
	}
	w.segs = append(w.segs, seg)
	size, height, err = w.walkContainer(child, depth+1)
	if err != nil {
		return 0, 0, err // keep segs: they locate the failure
	}
	w.segs = w.segs[:len(w.segs)-1]
	return size, height, nil
}

func (w *outputWalker) walkStringMap(m map[string]any, typ reflect.Type, depth int) (int, int, error) {
	n := len(m)
	if n == 0 {
		return 0, 0, nil
	}
	id := containerID{ptr: reflect.ValueOf(m).Pointer(), len: n, typ: typ}
	if c, cached, err := w.enter(id, depth); err != nil || cached {
		return c.size, c.height, err
	}
	total, maxChild := n, 0
	for k, c := range m {
		size, h, err := w.descend(c, pathSeg{key: k}, depth)
		if err != nil {
			return 0, 0, err
		}
		total += size
		maxChild = max(maxChild, h)
		if total > maxOutputEntries {
			return 0, 0, w.tooBig()
		}
	}
	res := checked{size: total, height: maxChild + 1}
	w.leave(id, res)
	return res.size, res.height, nil
}

func (w *outputWalker) walkAnySlice(s []any, depth int) (int, int, error) {
	n := len(s)
	if n == 0 {
		return 0, 0, nil
	}
	id := containerID{ptr: reflect.ValueOf(s).Pointer(), len: n, typ: typAnySlice}
	if c, cached, err := w.enter(id, depth); err != nil || cached {
		return c.size, c.height, err
	}
	total, maxChild := n, 0
	for i, c := range s {
		size, h, err := w.descend(c, pathSeg{index: i, isIdx: true}, depth)
		if err != nil {
			return 0, 0, err
		}
		total += size
		maxChild = max(maxChild, h)
		if total > maxOutputEntries {
			return 0, 0, w.tooBig()
		}
	}
	res := checked{size: total, height: maxChild + 1}
	w.leave(id, res)
	return res.size, res.height, nil
}

// walkValue is the reflection fallback for containers the fast path does not
// know. Its identity and limit rules are the same as the fast path's.
func (w *outputWalker) walkValue(rv reflect.Value, depth int) (int, int, error) {
	var n int
	switch rv.Kind() { //nolint:exhaustive // only containers can hold other values
	case reflect.Map:
		n = rv.Len()
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return 0, 0, nil // byte data, no nesting
		}
		n = rv.Len()
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return 0, 0, nil
		}
		return w.walkValue(rv.Elem(), depth)
	default:
		return 0, 0, nil
	}
	if n == 0 {
		return 0, 0, nil
	}

	var id containerID
	trackable := rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice
	if trackable {
		id = containerID{ptr: rv.Pointer(), len: n, typ: rv.Type()}
		if c, cached, err := w.enter(id, depth); err != nil || cached {
			return c.size, c.height, err
		}
	} else if depth >= maxOutputDepth {
		return 0, 0, w.tooDeep()
	}

	total, maxChild := n, 0
	add := func(child reflect.Value, seg pathSeg) error {
		size, h, err := w.descend(child.Interface(), seg, depth)
		if err != nil {
			return err
		}
		total += size
		maxChild = max(maxChild, h)
		if total > maxOutputEntries {
			return w.tooBig()
		}
		return nil
	}

	switch rv.Kind() { //nolint:exhaustive // checked above
	case reflect.Map:
		it := rv.MapRange()
		for it.Next() {
			if err := add(it.Value(), pathSeg{key: keyString(it.Key())}); err != nil {
				return 0, 0, err
			}
		}
	default:
		for i := 0; i < n; i++ {
			if err := add(rv.Index(i), pathSeg{index: i, isIdx: true}); err != nil {
				return 0, 0, err
			}
		}
	}

	res := checked{size: total, height: maxChild + 1}
	if trackable {
		w.leave(id, res)
	}
	return res.size, res.height, nil
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
