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

package schemaregistry

//go:generate mockgen -typed -destination=mock/schema_service.go -package=mock -mock_names=Service=Service . Service

import (
	"context"

	"github.com/conduitio/conduit-commons/schema"
)

type Service interface {
	Create(ctx context.Context, subject string, bytes []byte) (schema.Instance, error)
	Get(ctx context.Context, subject string, version int) (schema.Instance, error)

	Check(ctx context.Context) error
}
