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

package api

import (
	"context"
	"runtime"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
)

type Information struct {
	apiv1.UnimplementedInformationServiceServer
	version string
}

func (i *Information) GetInfo(context.Context, *apiv1.GetInfoRequest) (*apiv1.GetInfoResponse, error) {
	info := &apiv1.Info{
		Version: i.version,
		Os:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}

	return &apiv1.GetInfoResponse{Info: info}, nil
}

func NewInformation(version string) *Information {
	return &Information{version: version}
}

func (i *Information) Register(srv *grpc.Server) {
	apiv1.RegisterInformationServiceServer(srv, i)
}
