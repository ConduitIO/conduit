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
}

func testSetGet(t *testing.T, db DB) {
	is := is.New(t)

	t.Run(testName(), func(t *testing.T) {
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
	is := is.New(t)

	t.Run(testName(), func(t *testing.T) {
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
	is := is.New(t)

	t.Run(testName(), func(t *testing.T) {
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
	is := is.New(t)

	const valuesSize = 100
	t.Run(testName(), func(t *testing.T) {
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
			gotKeys, err := db.GetKeys(ctx, keyPrefix)
			is.NoErr(err)
			is.True(len(gotKeys) == valuesSize) // expects .GetKeys to return 100 keys

			sort.Strings(gotKeys) // sort so we can compare them
			is.Equal(wantKeys, gotKeys)
		})

		t.Run("emptyKeyPrefix", func(t *testing.T) {
			gotKeys, err := db.GetKeys(ctx, "")
			is.NoErr(err)
			is.True(len(gotKeys) == valuesSize+1) // expects .GetKeys to return 101 keys

			sort.Strings(gotKeys) // sort so we can compare them
			is.Equal(append([]string{"different prefix"}, wantKeys...), gotKeys)
		})

		t.Run("nonExistingPrefix", func(t *testing.T) {
			gotKeys, err := db.GetKeys(ctx, "non-existing-prefix")
			is.NoErr(err)
			is.Equal([]string(nil), gotKeys)
		})
	})
}

func testTransactionVisibility(t *testing.T, db DB) {
	is := is.New(t)

	t.Run(testName(), func(t *testing.T) {
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

// testName returns the name of the acceptance test (function name).
func testName() string {
	//nolint:dogsled // not important in tests
	pc, _, _, _ := runtime.Caller(1)
	caller := runtime.FuncForPC(pc).Name()
	return caller[strings.LastIndex(caller, ".")+1:]
}
