// Copyright © 2026 Meroxa, Inc.
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

package boundedfetch

import (
	"context"
	"io"
	"net/http"
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// ErrTooLarge is returned by Fetch/ReadFile when the response/file body
// exceeds the caller-specified maxBytes cap. Distinguished from a generic
// read failure so callers can map it to their own domain-specific "too
// large" conduiterr.Code — see the package doc for why this package
// registers none itself.
var ErrTooLarge = cerrors.New("boundedfetch: body exceeded the size cap")

// Fetch performs an HTTP GET on url and returns its body, capped at
// maxBytes: the body is wrapped in io.LimitReader(resp.Body, maxBytes+1),
// and reading exactly maxBytes+1 bytes back (rather than hitting EOF at or
// before maxBytes) proves the real payload exceeded the cap rather than
// happening to land exactly on it (P0-2).
func Fetch(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, cerrors.Errorf("could not build request for %q: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, cerrors.Errorf("could not fetch %q: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, cerrors.Errorf("fetching %q returned unexpected status %d", url, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, cerrors.Errorf("could not read response body for %q: %w", url, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// ReadFile reads a local file, capped at maxBytes, using the identical
// LimitReader-plus-one-byte technique as Fetch — so a local/offline-bundle
// read enforces the exact same bound as a network fetch.
func ReadFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, cerrors.Errorf("could not open %q: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, cerrors.Errorf("could not read %q: %w", path, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}
