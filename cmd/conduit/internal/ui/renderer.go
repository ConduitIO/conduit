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

package ui

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// Status is the three-way verdict Renderer.Glyph selects a glyph for. It
// mirrors check.Status in shape only — this package has no dependency on
// pkg/conduit/check (see the package doc), so a caller maps its own domain
// status onto this type at the render call site.
type Status int

const (
	StatusPass Status = iota
	StatusWarn
	StatusFail
)

// Glyph strings. Unicode is used on a color-capable TTY; ASCII is the
// fallback everywhere else (see NewRenderer).
const (
	glyphPassUnicode = "✓"
	glyphWarnUnicode = "⚠"
	glyphFailUnicode = "✗"
	glyphPassASCII   = "[OK]"
	glyphWarnASCII   = "[WARN]"
	glyphFailASCII   = "[FAIL]"
)

// ANSI SGR codes used when a Renderer has color enabled.
const (
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiReset  = "\x1b[0m"
)

// Renderer owns glyph and color selection for one output stream: Unicode
// glyphs with ANSI color when the stream is a color-capable TTY, the ASCII
// fallback ([OK]/[WARN]/[FAIL], no color) otherwise. This is the single
// place that makes that decision — no command should re-implement TTY or
// NO_COLOR detection itself (CLI output conventions §2).
type Renderer struct {
	// plain is true when Unicode glyphs and color are both disabled.
	plain bool
}

// NewRenderer detects whether w supports Unicode glyphs and color, and
// returns a Renderer that renders accordingly. It falls back to the plain
// ASCII, no-color mode when any of the following hold:
//
//   - noColor is true (the command's own --no-color flag);
//   - the NO_COLOR environment variable is set, to any value, including
//     empty (https://no-color.org — presence, not content, is the signal);
//   - w is not a terminal (e.g. output is redirected to a file or piped).
func NewRenderer(w io.Writer, noColor bool) *Renderer {
	_, noColorSet := os.LookupEnv("NO_COLOR")
	plain := noColor || noColorSet || !isTerminal(w)
	return &Renderer{plain: plain}
}

// isTerminal reports whether w is a terminal. A non-*os.File writer (a
// bytes.Buffer in a test, a pipe some other tool constructed) is never a
// terminal.
//
// It is a package-level variable, not a plain function, so the renderer
// test can stub it to exercise the TTY branch deterministically — there is
// no portable way to fabricate a real terminal fd in a unit test without a
// pty dependency this package has no other reason to take on.
var isTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// Glyph returns the glyph for status: colored Unicode, or the ASCII
// fallback, depending on how this Renderer was constructed. An out-of-range
// status is treated as StatusFail — conservatively surfacing a problem is
// safer than silently rendering nothing.
func (r *Renderer) Glyph(status Status) string {
	if r.plain {
		switch status {
		case StatusPass:
			return glyphPassASCII
		case StatusWarn:
			return glyphWarnASCII
		case StatusFail:
			return glyphFailASCII
		default:
			return glyphFailASCII
		}
	}

	switch status {
	case StatusPass:
		return ansiGreen + glyphPassUnicode + ansiReset
	case StatusWarn:
		return ansiYellow + glyphWarnUnicode + ansiReset
	case StatusFail:
		return ansiRed + glyphFailUnicode + ansiReset
	default:
		return ansiRed + glyphFailUnicode + ansiReset
	}
}

// Color reports whether this Renderer applies ANSI color.
func (r *Renderer) Color() bool { return !r.plain }

// Glyphs reports whether this Renderer uses Unicode glyphs, as opposed to
// the ASCII fallback.
func (r *Renderer) Glyphs() bool { return !r.plain }

// DiffAction is the three-way verdict DiffGlyph selects a symbol for:
// resource create/update/delete, as rendered by `pipelines deploy|apply`'s
// plan/diff output. It is a separate type from Status (rather than reusing
// StatusPass/Warn/Fail) because a diff's semantics are genuinely different —
// "created" is not "passed" — so a future change to one glyph set can't
// silently affect the other.
type DiffAction int

const (
	DiffCreate DiffAction = iota
	DiffUpdate
	DiffDelete
)

// Diff glyph strings. Unicode is used on a color-capable TTY; ASCII is the
// fallback everywhere else, matching Glyph's convention.
const (
	glyphDiffCreateUnicode = "+"
	glyphDiffUpdateUnicode = "~"
	glyphDiffDeleteUnicode = "-"
	glyphDiffCreateASCII   = "[+]"
	glyphDiffUpdateASCII   = "[~]"
	glyphDiffDeleteASCII   = "[-]"
)

// DiffGlyph returns the glyph for a plan/diff action: colored (green/yellow/
// red) on a color-capable TTY, or the bracketed ASCII fallback otherwise. An
// out-of-range action renders as DiffUpdate — the neutral middle ground,
// since a diff symbol (unlike Glyph's pass/fail) has no "conservatively
// surface a problem" bias to fall back on.
func (r *Renderer) DiffGlyph(action DiffAction) string {
	if r.plain {
		switch action {
		case DiffCreate:
			return glyphDiffCreateASCII
		case DiffDelete:
			return glyphDiffDeleteASCII
		case DiffUpdate:
			return glyphDiffUpdateASCII
		default:
			return glyphDiffUpdateASCII
		}
	}

	switch action {
	case DiffCreate:
		return ansiGreen + glyphDiffCreateUnicode + ansiReset
	case DiffDelete:
		return ansiRed + glyphDiffDeleteUnicode + ansiReset
	case DiffUpdate:
		return ansiYellow + glyphDiffUpdateUnicode + ansiReset
	default:
		return ansiYellow + glyphDiffUpdateUnicode + ansiReset
	}
}
