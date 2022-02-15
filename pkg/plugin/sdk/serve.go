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

package sdk

import (
	"fmt"
	"os"

	"github.com/conduitio/connector-plugin/cpluginv1"
	"github.com/conduitio/connector-plugin/cpluginv1/server"
)

func Serve(
	specFactory func() Specification,
	sourceFactory func() Source,
	destinationFactory func() Destination,
) {
	err := serve(specFactory, sourceFactory, destinationFactory)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error running plugin: %+v", err)
		os.Exit(1)
	}
}

func serve(
	specFactory func() Specification,
	sourceFactory func() Source,
	destinationFactory func() Destination,
) error {
	err := initStandaloneModeLogger()
	if err != nil {
		return err
	}

	if sourceFactory == nil {
		sourceFactory = func() Source { return nil }
	}
	if destinationFactory == nil {
		destinationFactory = func() Destination { return nil }
	}

	return server.Serve(
		func() cpluginv1.SpecifierPlugin { return NewSpecifierPlugin(specFactory()) },
		func() cpluginv1.SourcePlugin { return NewSourcePlugin(sourceFactory()) },
		func() cpluginv1.DestinationPlugin { return NewDestinationPlugin(destinationFactory()) },
	)
}
