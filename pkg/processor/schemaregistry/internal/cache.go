// Copyright © 2023 Meroxa, Inc.
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

package internal

import (
	"sync"

	"github.com/twmb/franz-go/pkg/sr"
)

type (
	subject     string
	version     int
	fingerprint uint64
)

// SchemaCache caches schemas by ID, their subject/fingerprint and
// subject/version. Fingerprints are calculated using the Rabin algorithm.
type SchemaCache struct {
	initOnce sync.Once

	idCache                 map[int]sr.Schema
	subjectFingerprintCache map[subject]map[fingerprint]sr.SubjectSchema
	subjectVersionCache     map[subject]map[version]sr.SubjectSchema

	m sync.RWMutex
}

func (c *SchemaCache) init() {
	c.initOnce.Do(func() {
		c.idCache = make(map[int]sr.Schema)
		c.subjectFingerprintCache = make(map[subject]map[fingerprint]sr.SubjectSchema)
		c.subjectVersionCache = make(map[subject]map[version]sr.SubjectSchema)
	})
}

func (c *SchemaCache) AddSubjectSchema(ss sr.SubjectSchema) {
	c.init()
	c.m.Lock()
	defer c.m.Unlock()

	c.idCache[ss.ID] = ss.Schema

	versionCache, ok := c.subjectVersionCache[subject(ss.Subject)]
	if !ok {
		versionCache = make(map[version]sr.SubjectSchema)
		c.subjectVersionCache[subject(ss.Subject)] = versionCache
	}
	versionCache[version(ss.Version)] = ss

	fingerprintCache, ok := c.subjectFingerprintCache[subject(ss.Subject)]
	if !ok {
		fingerprintCache = make(map[fingerprint]sr.SubjectSchema)
		c.subjectFingerprintCache[subject(ss.Subject)] = fingerprintCache
	}
	fp := Rabin([]byte(ss.Schema.Schema))
	fingerprintCache[fingerprint(fp)] = ss
}

func (c *SchemaCache) AddSchema(id int, s sr.Schema) {
	c.init()
	c.m.Lock()
	defer c.m.Unlock()

	c.idCache[id] = s
}

func (c *SchemaCache) GetByID(id int) (sr.Schema, bool) {
	c.init()
	c.m.RLock()
	defer c.m.RUnlock()

	s, ok := c.idCache[id]
	return s, ok
}

func (c *SchemaCache) GetBySubjectText(s string, text string) (sr.SubjectSchema, bool) {
	c.init()
	c.m.RLock()
	defer c.m.RUnlock()

	fingerprintCache, ok := c.subjectFingerprintCache[subject(s)]
	if !ok {
		return sr.SubjectSchema{}, false
	}
	fp := Rabin([]byte(text))
	ss, ok := fingerprintCache[fingerprint(fp)]
	return ss, ok
}

func (c *SchemaCache) GetBySubjectVersion(s string, v int) (sr.SubjectSchema, bool) {
	c.init()
	c.m.RLock()
	defer c.m.RUnlock()

	versionCache, ok := c.subjectVersionCache[subject(s)]
	if !ok {
		return sr.SubjectSchema{}, false
	}
	ss, ok := versionCache[version(v)]
	return ss, ok
}
