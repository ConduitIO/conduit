package log

import (
	"io"
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/rs/zerolog"
)

type Format int

const (
	FormatCLI Format = iota
	FormatJSON
)

// ParseFormat converts a format string into a log Format value.
// returns an error if the input string does not match known values.
func ParseFormat(format string) (Format, error) {
	switch {
	case format == "cli":
		return FormatCLI, nil
	case format == "json":
		return FormatJSON, nil
	default:
		return -1, cerrors.Errorf("unsupported log format: %s", format)
	}
}

// GetWriter returns a writer according to the log Format
func GetWriter(f Format) io.Writer {
	var w io.Writer = os.Stdout
	if f == FormatCLI {
		cw := zerolog.NewConsoleWriter()
		cw.TimeFormat = "2006-01-02T15:04:05+00:00"
		w = cw
	}
	return w
}
