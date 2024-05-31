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

package schema

import (
	"context"
	"github.com/conduitio/conduit-commons/schema"
)

type ApicurioService struct {
}

func NewApicurioService() *ApicurioService {
	return &ApicurioService{}
}

func (a ApicurioService) Create(ctx context.Context, name string, bytes []byte) (schema.Instance, error) {
	//TODO implement me
	panic("implement me")
}

func (a ApicurioService) Get(ctx context.Context, id string) (schema.Instance, error) {
	//TODO implement me
	panic("implement me")
}
