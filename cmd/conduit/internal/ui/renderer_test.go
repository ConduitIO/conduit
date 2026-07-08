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
	"bytes"
	"io"
	"testing"

	"github.com/matryer/is"
)

// withTerminal stubs the package's terminal-detection for the duration of a
// test, restoring it on cleanup. It's the only portable way to exercise the
// TTY branch without a real pty.
func withTerminal(t *testing.T, tty bool) {
	t.Helper()
	orig := isTerminal
	isTerminal = func(io.Writer) bool { return tty }
	t.Cleanup(func() { isTerminal = orig })
}

func TestNewRenderer_TTYNoColorEnvUnset_UsesGlyphsAndColor(t *testing.T) {
	is := is.New(t)
	withTerminal(t, true)

	r := NewRenderer(&bytes.Buffer{}, false)

	is.True(r.Glyphs())
	is.True(r.Color())
	is.Equal(r.Glyph(StatusPass), ansiGreen+glyphPassUnicode+ansiReset)
}

func TestNewRenderer_NonTTY_UsesASCIIFallback(t *testing.T) {
	is := is.New(t)
	withTerminal(t, false)

	r := NewRenderer(&bytes.Buffer{}, false)

	is.True(!r.Glyphs())
	is.True(!r.Color())
	is.Equal(r.Glyph(StatusFail), glyphFailASCII)
}

func TestNewRenderer_NoColorFlag_UsesASCIIFallbackEvenOnTTY(t *testing.T) {
	is := is.New(t)
	withTerminal(t, true) // would otherwise select Unicode+color

	r := NewRenderer(&bytes.Buffer{}, true) // --no-color

	is.True(!r.Glyphs())
	is.True(!r.Color())
}

func TestNewRenderer_NOCOLOREnv_UsesASCIIFallbackEvenOnTTY(t *testing.T) {
	is := is.New(t)
	withTerminal(t, true) // would otherwise select Unicode+color
	t.Setenv("NO_COLOR", "")

	r := NewRenderer(&bytes.Buffer{}, false)

	is.True(!r.Glyphs())
	is.True(!r.Color())
}

// TestNewRenderer_NOCOLOREnv_PresenceNotContent asserts NO_COLOR's *value*
// doesn't matter, only whether it's set — per https://no-color.org, an
// explicit empty value still means "no color".
func TestNewRenderer_NOCOLOREnv_PresenceNotContent(t *testing.T) {
	is := is.New(t)
	withTerminal(t, true)
	t.Setenv("NO_COLOR", "0") // a value some tools would misread as falsy

	r := NewRenderer(&bytes.Buffer{}, false)

	is.True(!r.Glyphs())
	is.True(!r.Color())
}

func TestRenderer_Glyph_AllStatuses(t *testing.T) {
	t.Run("unicode", func(t *testing.T) {
		is := is.New(t)
		withTerminal(t, true)
		r := NewRenderer(&bytes.Buffer{}, false)

		is.Equal(r.Glyph(StatusPass), ansiGreen+glyphPassUnicode+ansiReset)
		is.Equal(r.Glyph(StatusWarn), ansiYellow+glyphWarnUnicode+ansiReset)
		is.Equal(r.Glyph(StatusFail), ansiRed+glyphFailUnicode+ansiReset)
	})

	t.Run("ascii", func(t *testing.T) {
		is := is.New(t)
		withTerminal(t, false)
		r := NewRenderer(&bytes.Buffer{}, false)

		is.Equal(r.Glyph(StatusPass), glyphPassASCII)
		is.Equal(r.Glyph(StatusWarn), glyphWarnASCII)
		is.Equal(r.Glyph(StatusFail), glyphFailASCII)
	})
}
