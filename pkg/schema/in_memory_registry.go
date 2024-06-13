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

package schema

import (
	"sync"

	"github.com/lovromazgon/franz-go/pkg/sr"
)

// InMemoryRegistry is a simple fake registry meant to be used in tests. It stores
// schemas in memory and supports only the basic functionality needed in our
// tests and supported by our client.
type InMemoryRegistry struct {
	schemas            []sr.SubjectSchema
	fingerprintIDCache map[uint64]int
	idSequence         int

	m sync.Mutex
}

func NewInMemoryRegistry() *InMemoryRegistry {
	return &InMemoryRegistry{
		schemas:            make([]sr.SubjectSchema, 0),
		fingerprintIDCache: make(map[uint64]int),
	}
}

func (r *InMemoryRegistry) CreateSchema(subject string, schema sr.Schema) sr.SubjectSchema {
	r.m.Lock()
	defer r.m.Unlock()

	fp := Rabin([]byte(schema.Schema))
	id, ok := r.fingerprintIDCache[fp]
	if ok {
		// schema exists, see if subject matches
		ss, ok := r.findBySubjectID(subject, id)
		if ok {
			// schema exists for this subject, return it
			return ss
		}
	}
	if !ok {
		// schema does not exist yet
		id = r.nextID()
	}
	version := r.nextVersion(subject)

	ss := sr.SubjectSchema{
		Subject: subject,
		Version: version,
		ID:      id,
		Schema:  schema,
	}

	r.schemas = append(r.schemas, ss)
	r.fingerprintIDCache[fp] = id

	return ss
}

func (r *InMemoryRegistry) SchemaByID(id int) (sr.Schema, bool) {
	r.m.Lock()
	defer r.m.Unlock()

	s, ok := r.findOneByID(id)
	return s, ok
}

func (r *InMemoryRegistry) SchemaBySubjectVersion(subject string, version int) (sr.SubjectSchema, bool) {
	r.m.Lock()
	defer r.m.Unlock()

	return r.findBySubjectVersion(subject, version)
}

func (r *InMemoryRegistry) SubjectVersionsByID(id int) []sr.SubjectSchema {
	r.m.Lock()
	defer r.m.Unlock()

	return r.findAllByID(id)
}

func (r *InMemoryRegistry) nextID() int {
	r.idSequence++
	return r.idSequence
}

func (r *InMemoryRegistry) nextVersion(subject string) int {
	return len(r.findBySubject(subject)) + 1
}

func (r *InMemoryRegistry) findBySubject(subject string) []sr.SubjectSchema {
	var sss []sr.SubjectSchema
	for _, ss := range r.schemas {
		if ss.Subject == subject {
			sss = append(sss, ss)
		}
	}
	return sss
}

func (r *InMemoryRegistry) findOneByID(id int) (sr.Schema, bool) {
	for _, ss := range r.schemas {
		if ss.ID == id {
			return ss.Schema, true
		}
	}
	return sr.Schema{}, false
}

func (r *InMemoryRegistry) findAllByID(id int) []sr.SubjectSchema {
	var sss []sr.SubjectSchema
	for _, ss := range r.schemas {
		if ss.ID == id {
			sss = append(sss, ss)
		}
	}
	return sss
}

func (r *InMemoryRegistry) findBySubjectID(subject string, id int) (sr.SubjectSchema, bool) {
	for _, ss := range r.schemas {
		if ss.Subject == subject && ss.ID == id {
			return ss, true
		}
	}
	return sr.SubjectSchema{}, false
}

func (r *InMemoryRegistry) findBySubjectVersion(subject string, version int) (sr.SubjectSchema, bool) {
	for _, ss := range r.schemas {
		if ss.Subject == subject && ss.Version == version {
			return ss, true
		}
	}
	return sr.SubjectSchema{}, false
}
