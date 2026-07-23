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
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// maxExtractedBytes bounds the TOTAL decompressed size ExtractBinary will
// write, independent of the (already size-capped, per the index's declared
// Artifact.Size) compressed input — a decompression-bomb guard: a small
// compressed file can still expand to an enormous one.
const maxExtractedBytes = 1 << 30 // 1 GiB

// ExtractBinary opens the tar.gz archive at archivePath and extracts the
// single root-level regular file it must contain into destDir, returning
// its path. It refuses, with CodeArchiveInvalid, rather than guesses, on
// every one of the following:
//
//   - A path-traversal entry: an absolute path, or a relative path that
//     escapes destDir once cleaned (e.g. "../../etc/passwd").
//   - A symlink or hardlink entry: a malicious archive must not be able to
//     point extraction at an arbitrary target via a link.
//   - Zero or more than one candidate root-level regular file: this
//     pipeline installs exactly one connector binary per artifact, so an
//     ambiguous archive is refused, not guessed at. Non-root-level regular
//     files (e.g. a LICENSE nested in a subdirectory) are extracted-and-
//     ignored as far as "candidate" status goes, so an archive that
//     legitimately bundles extra files alongside the binary at the root
//     still resolves unambiguously.
//   - An archive that expands past maxExtractedBytes.
func ExtractBinary(archivePath, destDir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", conduiterr.Wrap(CodeArchiveInvalid, "could not open downloaded archive", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", conduiterr.Wrap(CodeArchiveInvalid, "downloaded archive is not valid gzip", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	var candidate string
	var extractedTotal int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", conduiterr.Wrap(CodeArchiveInvalid, "downloaded archive is corrupt", err)
		}

		cleanName := filepath.Clean(hdr.Name)
		if filepath.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
			return "", conduiterr.New(CodeArchiveInvalid, fmt.Sprintf(
				"archive entry %q attempts to escape the extraction directory", hdr.Name))
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			continue // directory entries carry nothing to extract themselves
		case tar.TypeSymlink, tar.TypeLink:
			return "", conduiterr.New(CodeArchiveInvalid, fmt.Sprintf(
				"archive entry %q is a symlink/hardlink, which is not permitted", hdr.Name))
		case tar.TypeReg:
			isRoot := !strings.Contains(cleanName, string(filepath.Separator))

			destPath := filepath.Join(destDir, cleanName)
			if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
				return "", conduiterr.Wrap(CodeArchiveInvalid, "could not create extraction directory", err)
			}
			out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
			if err != nil {
				return "", conduiterr.Wrap(CodeArchiveInvalid, "could not create extracted file", err)
			}
			n, copyErr := io.Copy(out, io.LimitReader(tr, maxExtractedBytes-extractedTotal+1))
			closeErr := out.Close()
			if copyErr != nil {
				return "", conduiterr.Wrap(CodeArchiveInvalid, "could not extract archive entry", copyErr)
			}
			if closeErr != nil {
				return "", conduiterr.Wrap(CodeArchiveInvalid, "could not finalize extracted file", closeErr)
			}
			extractedTotal += n
			if extractedTotal > maxExtractedBytes {
				return "", conduiterr.New(CodeArchiveInvalid, "archive expands past the maximum allowed decompressed size")
			}

			if !isRoot {
				continue // extracted (for completeness) but not a binary candidate
			}
			if candidate != "" {
				return "", conduiterr.New(CodeArchiveInvalid, fmt.Sprintf(
					"archive has more than one candidate binary at its root (%q and %q)", candidate, cleanName))
			}
			candidate = cleanName
		default:
			continue // ignore other entry types (e.g. pax extended headers) rather than refusing outright
		}
	}

	if candidate == "" {
		return "", conduiterr.New(CodeArchiveInvalid, "archive contains no root-level regular file to install")
	}
	return filepath.Join(destDir, candidate), nil
}
