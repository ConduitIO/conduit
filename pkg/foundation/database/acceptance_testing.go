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

package database

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/csync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/google/uuid"
	"github.com/matryer/is"
)

// AcceptanceTest is the acceptance test that all implementations of DB should
// pass. It should manually be called from a test case in each implementation:
//
//	func TestDB(t *testing.T) {
//	    db = NewDB()
//	    database.AcceptanceTest(t, db)
//	}
func AcceptanceTest(t *testing.T, db DB) {
	testSetGet(t, db)
	testUpdate(t, db)
	testDelete(t, db)
	testGetKeys(t, db)
	testTransactionVisibility(t, db)
	testConcurrency(t, db)
}

func testSetGet(t *testing.T, db DB) {
	t.Run(testName(), func(t *testing.T) {
		is := is.New(t)
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		is.NoErr(err)
		defer txn.Discard()

		//nolint:goconst // we can turn off this check for test files
		key := "my-key"
		want := []byte(uuid.NewString())
		err = db.Set(ctx, key, want)
		is.NoErr(err)

		got, err := db.Get(ctx, key)
		is.NoErr(err)
		is.Equal(want, got)
	})
}

func testUpdate(t *testing.T, db DB) {
	t.Run(testName(), func(t *testing.T) {
		is := is.New(t)
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		is.NoErr(err)
		defer txn.Discard()

		key := "my-key"
		want := []byte(uuid.NewString())

		err = db.Set(ctx, key, []byte("do not want this"))
		is.NoErr(err)

		err = db.Set(ctx, key, want)
		is.NoErr(err)

		got, err := db.Get(ctx, key)
		is.NoErr(err)
		is.Equal(want, got)
	})
}

func testDelete(t *testing.T, db DB) {
	t.Run(testName(), func(t *testing.T) {
		is := is.New(t)
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		is.NoErr(err)
		defer txn.Discard()

		key := "my-key"
		value := []byte(uuid.NewString())

		err = db.Set(ctx, key, value)
		is.NoErr(err)

		err = db.Set(ctx, key, nil)
		is.NoErr(err)

		got, err := db.Get(ctx, key)
		is.True(got == nil)
		is.True(cerrors.Is(err, ErrKeyNotExist)) // expected error for non-existing key
	})
}

func testGetKeys(t *testing.T, db DB) {
	const valuesSize = 100
	t.Run(testName(), func(t *testing.T) {
		is := is.New(t)
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		is.NoErr(err)
		defer txn.Discard()

		keyPrefix := "key"
		var wantKeys []string
		for i := 0; i < valuesSize; i++ {
			key := fmt.Sprintf("key%2d", i)
			wantKeys = append(wantKeys, key)
			err := db.Set(ctx, key, []byte(strconv.Itoa(i)))
			is.NoErr(err)
		}
		err = db.Set(ctx, "different prefix", []byte("should not be returned"))
		is.NoErr(err)

		t.Run("withKeyPrefix", func(t *testing.T) {
			is := is.New(t)
			gotKeys, err := db.GetKeys(ctx, keyPrefix)
			is.NoErr(err)
			is.True(len(gotKeys) == valuesSize) // expects .GetKeys to return 100 keys

			sort.Strings(gotKeys) // sort so we can compare them
			is.Equal(wantKeys, gotKeys)
		})

		t.Run("emptyKeyPrefix", func(t *testing.T) {
			is := is.New(t)
			gotKeys, err := db.GetKeys(ctx, "")
			is.NoErr(err)
			is.True(len(gotKeys) == valuesSize+1) // expects .GetKeys to return 101 keys

			sort.Strings(gotKeys) // sort so we can compare them
			is.Equal(append([]string{"different prefix"}, wantKeys...), gotKeys)
		})

		t.Run("nonExistingPrefix", func(t *testing.T) {
			is := is.New(t)
			gotKeys, err := db.GetKeys(ctx, "non-existing-prefix")
			is.NoErr(err)
			is.Equal([]string(nil), gotKeys)
		})
	})
}

func testTransactionVisibility(t *testing.T, db DB) {
	t.Run(testName(), func(t *testing.T) {
		is := is.New(t)
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		is.NoErr(err)
		defer txn.Discard()

		key := "my-key"
		want := []byte("my-value")
		err = db.Set(ctx, key, want)
		is.NoErr(err)

		// get the key outside of the transaction
		got, err := db.Get(context.Background(), key)
		is.True(got == nil)
		is.True(cerrors.Is(err, ErrKeyNotExist)) // expected error for non-existing key

		err = txn.Commit()
		is.NoErr(err)
		defer db.Set(context.Background(), key, nil) //nolint:errcheck // cleanup

		// key should be visible now
		got, err = db.Get(context.Background(), key)
		is.NoErr(err)
		is.Equal(want, got)
	})
}

func testConcurrency(t *testing.T, db DB) {
	const (
		workers = 100
		loops   = 100
	)

	t.Run(testName(), func(t *testing.T) {
		ctx := context.Background()
		is := is.New(t)

		iterationFn := func(ctx context.Context, workerID, iteration int) (err error) {
			if iteration%2 == 0 {
				// every other iteration is executed in a transaction
				var tx Transaction
				tx, ctx, err = db.NewTransaction(ctx, true)
				if err != nil {
					return fmt.Errorf("expected no error when creating transaction, got: %w", err)
				}
				defer func() {
					if err != nil || iteration%4 == 0 {
						// discard every other transaction
						tx.Discard()
						return
					}
					err = tx.Commit()
					if err != nil {
						err = fmt.Errorf("expected no error when committing transaction, got: %w", err)
					}
				}()
			}

			key := fmt.Sprintf("key-%d-%d", workerID, iteration)
			val := []byte(fmt.Sprintf("value-%d-%d", workerID, iteration))
			_, err = db.Get(ctx, key)
			if !cerrors.Is(err, ErrKeyNotExist) {
				return fmt.Errorf("expected error when getting value for key %q, got: %w", key, err)
			}
			err = db.Set(ctx, key, val)
			if err != nil {
				return fmt.Errorf("expected no error when setting value for key %q, got: %w", key, err)
			}
			got, err := db.Get(ctx, key)
			if err != nil {
				return fmt.Errorf("expected no error when getting value for key %q, got: %w", key, err)
			}
			if !bytes.Equal(val, got) {
				return fmt.Errorf("expected value %q for key %q, got: %q", string(val), key, string(got))
			}
			return nil
		}

		var wg csync.WaitGroup
		wg.Add(workers)
		errs := make([]error, workers)

		for i := range workers {
			go func(i int) {
				defer wg.Done()
				for j := range loops {
					err := iterationFn(ctx, i, j)
					if err != nil {
						errs[i] = err
						return
					}
				}
			}(i)
		}
		err := wg.WaitTimeout(ctx, time.Second*10)
		is.NoErr(err)
		is.NoErr(cerrors.Join(errs...))
	})
}

// testName returns the name of the acceptance test (function name).
func testName() string {
	//nolint:dogsled // not important in tests
	pc, _, _, _ := runtime.Caller(1)
	caller := runtime.FuncForPC(pc).Name()
	return caller[strings.LastIndex(caller, ".")+1:]
}
