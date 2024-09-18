// Copyright © 2024 Meroxa, Inc.
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

package funnel

import (
	"context"
	"sync"

	"gopkg.in/tomb.v2"

	"github.com/conduitio/conduit-commons/opencdc"
)

type Task interface {
	// ID returns the identifier of this Task. Each Task in a pipeline must be
	// uniquely identified by the ID.
	ID() string

	Open(ctx context.Context) error
	Do(ctx context.Context, data []opencdc.Record, next Tasks) ([]opencdc.Record, error)
	Close(ctx context.Context) error
}

type Tasks []Task

func (t Tasks) Do(ctx context.Context, data []opencdc.Record) ([]opencdc.Record, error) {
	var next Tasks
	if len(t) > 1 {
		next = t[1:]
	}
	return t[0].Do(ctx, data, next)
}

// Worker is a collection of tasks that are executed sequentially. It is safe
// for concurrent use.
type Worker struct {
	tasks Tasks
	m     sync.Mutex
}

func NewWorker(tasks Tasks) *Worker {
	return &Worker{
		tasks: tasks,
	}
}

func (w *Worker) Open(ctx context.Context) error {
	t, ctx := tomb.WithContext(ctx) // TODO use conc instead
	for _, task := range w.tasks {
		t.Go(func() error {
			return task.Open(context.Background()) // TODO use proper context
		})
	}
	return t.Wait()
}

func (w *Worker) Do(ctx context.Context, data []opencdc.Record) ([]opencdc.Record, error) {
	// Lock the worker to prevent concurrent access to the tasks.
	w.m.Lock()
	defer w.m.Unlock()

	return w.tasks.Do(ctx, data)
}

// func (w *Worker) Do(ctx context.Context, data []opencdc.Record, next Tasks) ([]opencdc.Record, error) {
// 	// Lock the worker to prevent concurrent access to the tasks.
// 	w.m.Lock()
// 	defer w.m.Unlock()
//
// 	tasks := w.tasks
// 	if len(next) > 0 {
// 		if cap(w.tasks) < len(w.tasks)+len(next) {
// 			// allocate a new slice with extra slots for next tasks, so we don't have to
// 			// reallocate the slice every time we call it with next tasks
// 			tasks = make(Tasks, len(w.tasks), len(w.tasks)+len(next))
// 			copy(tasks, w.tasks)
// 			w.tasks = tasks
// 		}
// 		tasks = append(tasks, next...)
// 	}
//
// 	return tasks.Do(ctx, data)
// }
