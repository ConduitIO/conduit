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

package inspector

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

func BenchmarkInspector_NoSession_Send(b *testing.B) {
	ins := New(log.Nop(), 10)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ins.Send(context.Background(), []opencdc.Record{{Position: opencdc.Position("test-pos")}})
	}
}

func BenchmarkInspector_SingleSession_Send(b *testing.B) {
	ins := New(log.Nop(), 10)
	ins.NewSession(context.Background(), "test-id")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ins.Send(context.Background(), []opencdc.Record{{Position: opencdc.Position("test-pos")}})
	}
}

func BenchmarkInspector_10Sessions_Send(b *testing.B) {
	ins := New(log.Nop(), 10)
	for i := 0; i < 10; i++ {
		ins.NewSession(context.Background(), "test-id")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ins.Send(context.Background(), []opencdc.Record{{Position: opencdc.Position("test-pos")}})
	}
}
