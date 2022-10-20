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
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/google/uuid"
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
}

func testSetGet(t *testing.T, db DB) {
	t.Run(testName(), func(t *testing.T) {
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		assert.Ok(t, err)
		defer txn.Discard()

		//nolint:goconst // we can turn off this check for test files
		key := "my-key"
		want := []byte(uuid.NewString())
		err = db.Set(ctx, key, want)
		assert.Ok(t, err)

		got, err := db.Get(ctx, key)
		assert.Ok(t, err)
		assert.Equal(t, want, got)
	})
}

func testUpdate(t *testing.T, db DB) {
	t.Run(testName(), func(t *testing.T) {
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		assert.Ok(t, err)
		defer txn.Discard()

		key := "my-key"
		want := []byte(uuid.NewString())

		err = db.Set(ctx, key, []byte("do not want this"))
		assert.Ok(t, err)

		err = db.Set(ctx, key, want)
		assert.Ok(t, err)

		got, err := db.Get(ctx, key)
		assert.Ok(t, err)
		assert.Equal(t, want, got)
	})
}

func testDelete(t *testing.T, db DB) {
	t.Run(testName(), func(t *testing.T) {
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		assert.Ok(t, err)
		defer txn.Discard()

		key := "my-key"
		value := []byte(uuid.NewString())

		err = db.Set(ctx, key, value)
		assert.Ok(t, err)

		err = db.Set(ctx, key, nil)
		assert.Ok(t, err)

		got, err := db.Get(ctx, key)
		assert.Nil(t, got)
		assert.True(t, cerrors.Is(err, ErrKeyNotExist), "expected error for non-existing key")
	})
}

func testGetKeys(t *testing.T, db DB) {
	const valuesSize = 100
	t.Run(testName(), func(t *testing.T) {
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		assert.Ok(t, err)
		defer txn.Discard()

		keyPrefix := "key"
		var wantKeys []string
		for i := 0; i < valuesSize; i++ {
			key := fmt.Sprintf("key%2d", i)
			wantKeys = append(wantKeys, key)
			err := db.Set(ctx, key, []byte(strconv.Itoa(i)))
			assert.Ok(t, err)
		}
		err = db.Set(ctx, "different prefix", []byte("should not be returned"))
		assert.Ok(t, err)

		t.Run("withKeyPrefix", func(t *testing.T) {
			gotKeys, err := db.GetKeys(ctx, keyPrefix)
			assert.Ok(t, err)
			assert.True(t, len(gotKeys) == valuesSize, "expected %d keys, got %d", valuesSize, len(gotKeys))

			sort.Strings(gotKeys) // sort so we can compare them
			assert.Equal(t, wantKeys, gotKeys)
		})

		t.Run("emptyKeyPrefix", func(t *testing.T) {
			gotKeys, err := db.GetKeys(ctx, "")
			assert.Ok(t, err)
			assert.True(t, len(gotKeys) == valuesSize+1, "expected %d keys, got %d", valuesSize+1, len(gotKeys))

			sort.Strings(gotKeys) // sort so we can compare them
			assert.Equal(t, append([]string{"different prefix"}, wantKeys...), gotKeys)
		})

		t.Run("nonExistingPrefix", func(t *testing.T) {
			gotKeys, err := db.GetKeys(ctx, "non-existing-prefix")
			assert.Ok(t, err)
			assert.Equal(t, []string(nil), gotKeys)
		})
	})
}

func testTransactionVisibility(t *testing.T, db DB) {
	t.Run(testName(), func(t *testing.T) {
		txn, ctx, err := db.NewTransaction(context.Background(), true)
		assert.Ok(t, err)
		defer txn.Discard()

		key := "my-key"
		want := []byte("my-value")
		err = db.Set(ctx, key, want)
		assert.Ok(t, err)

		// get the key outside of the transaction
		got, err := db.Get(context.Background(), key)
		assert.Nil(t, got)
		assert.True(t, cerrors.Is(err, ErrKeyNotExist), "expected error for non-existing key")

		err = txn.Commit()
		assert.Ok(t, err)
		defer db.Set(context.Background(), key, nil) //nolint:errcheck // cleanup

		// key should be visible now
		got, err = db.Get(context.Background(), key)
		assert.Ok(t, err)
		assert.Equal(t, want, got)
	})
}

// testName returns the name of the acceptance test (function name).
func testName() string {
	//nolint:dogsled // not important in tests
	pc, _, _, _ := runtime.Caller(1)
	caller := runtime.FuncForPC(pc).Name()
	return caller[strings.LastIndex(caller, ".")+1:]
}
