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
	"fmt"
	"sync"

	"github.com/conduitio/conduit-commons/rabin"
	"github.com/twmb/franz-go/pkg/sr"
	"github.com/twmb/go-cache/cache"
)

type (
	subjectVersion     string
	subjectFingerprint string
)

func newSubjectVersion(subject string, version int) subjectVersion {
	return subjectVersion(fmt.Sprintf("%s:%d", subject, version))
}

func newSubjectFingerprint(subject string, text string) subjectFingerprint {
	fingerprint := rabin.Bytes([]byte(text))
	return subjectFingerprint(fmt.Sprintf("%s:%d", subject, fingerprint))
}

// SchemaCache caches schemas by ID, their subject/fingerprint and
// subject/version. Fingerprints are calculated using the Rabin algorithm.
type SchemaCache struct {
	initOnce sync.Once

	idCache                 *cache.Cache[int, sr.Schema]
	subjectFingerprintCache *cache.Cache[subjectFingerprint, sr.SubjectSchema]
	subjectVersionCache     *cache.Cache[subjectVersion, sr.SubjectSchema]
}

func (c *SchemaCache) init() {
	c.initOnce.Do(func() {
		c.idCache = cache.New[int, sr.Schema]()
		c.subjectFingerprintCache = cache.New[subjectFingerprint, sr.SubjectSchema]()
		c.subjectVersionCache = cache.New[subjectVersion, sr.SubjectSchema]()
	})
}

func (c *SchemaCache) GetByID(id int, miss func() (sr.Schema, error)) (sr.Schema, error) {
	c.init()
	s, err, _ := c.idCache.Get(id, miss)
	return s, err
}

func (c *SchemaCache) GetBySubjectText(subject string, text string, miss func() (sr.SubjectSchema, error)) (sr.SubjectSchema, error) {
	c.init()
	sfp := newSubjectFingerprint(subject, text)
	ss, err, _ := c.subjectFingerprintCache.Get(sfp, func() (sr.SubjectSchema, error) {
		ss, err := miss()
		if err != nil {
			return ss, err
		}
		c.idCache.Set(ss.ID, ss.Schema)
		c.subjectVersionCache.Set(newSubjectVersion(ss.Subject, ss.Version), ss)
		return ss, nil
	})
	return ss, err
}

func (c *SchemaCache) GetBySubjectVersion(subject string, version int, miss func() (sr.SubjectSchema, error)) (sr.SubjectSchema, error) {
	c.init()
	sv := newSubjectVersion(subject, version)
	ss, err, _ := c.subjectVersionCache.Get(sv, func() (sr.SubjectSchema, error) {
		ss, err := miss()
		if err != nil {
			return ss, err
		}
		c.idCache.Set(ss.ID, ss.Schema)
		c.subjectFingerprintCache.Set(newSubjectFingerprint(ss.Subject, ss.Schema.Schema), ss)
		return ss, nil
	})
	return ss, err
}
