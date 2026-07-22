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

package registry

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// maxRedirects bounds a download's redirect chain (e.g. following a Scarf
// gateway to the underlying GitHub Releases asset) so a redirect loop fails
// fast with CodeDownloadFailed instead of hanging or looping forever.
const maxRedirects = 10

// newDownloadClient returns an http.Client whose CheckRedirect refuses past
// maxRedirects hops.
func newDownloadClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
}

// DownloadResult is what Download reports: the digest computed from the
// bytes actually received (input to CheckCorruption, and later
// ArtifactRef.Digest — never re-read from disk after the fact) and the
// exact byte count written.
type DownloadResult struct {
	Path   string
	Digest [32]byte
	Size   int64
}

// Download streams url's response body into destPath (created exclusively,
// 0600 — refuses if destPath already exists, since every staging path is
// meant to be freshly created by the caller), computing a sha256 digest
// incrementally over the same bytes as they are written — never buffering
// the whole artifact in memory. The write is capped at maxBytes+1: reading
// exactly maxBytes+1 bytes back (rather than hitting EOF at or before
// maxBytes) proves the real payload exceeds the index's declared size
// rather than happening to land exactly on it (the same technique
// pkg/registry/boundedfetch uses for the index and bundle fetches — applied
// here directly, rather than via that package, because an artifact is
// streamed to disk rather than buffered into a []byte).
//
// Download does not compare the digest to any expected value — that is
// CheckCorruption's job, deliberately kept separate so the "this is
// integrity, not trust" distinction (Decision §3 step 5a) stays visible at
// the call site instead of being buried inside this function.
func Download(ctx context.Context, client *http.Client, url, destPath string, maxBytes int64) (DownloadResult, error) {
	if client == nil {
		client = newDownloadClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return DownloadResult{}, conduiterr.Wrap(CodeDownloadFailed, "could not build download request", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{}, conduiterr.Wrap(CodeDownloadFailed, fmt.Sprintf("could not download %q", url), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DownloadResult{}, conduiterr.New(CodeDownloadFailed, fmt.Sprintf(
			"downloading %q returned unexpected status %d", url, resp.StatusCode))
	}

	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return DownloadResult{}, conduiterr.Wrap(CodeDownloadFailed, fmt.Sprintf("could not create staging file %q", destPath), err)
	}
	defer f.Close()

	h := sha256.New()
	limited := io.LimitReader(resp.Body, maxBytes+1)
	n, err := io.Copy(f, io.TeeReader(limited, h))
	if err != nil {
		return DownloadResult{}, conduiterr.Wrap(CodeDownloadFailed, fmt.Sprintf("could not write staged download %q", destPath), err)
	}
	if n > maxBytes {
		return DownloadResult{}, conduiterr.New(CodeDownloadFailed, fmt.Sprintf(
			"downloaded artifact exceeds the declared size of %d bytes", maxBytes))
	}
	if err := f.Sync(); err != nil {
		return DownloadResult{}, conduiterr.Wrap(CodeDownloadFailed, "could not sync staged download to disk", err)
	}

	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return DownloadResult{Path: destPath, Digest: digest, Size: n}, nil
}
