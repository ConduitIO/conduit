package root

import (
	"testing"

	"github.com/conduitio/ecdysis"
	"github.com/matryer/is"
	"github.com/spf13/pflag"
)

func TestRootCommandFlags(t *testing.T) {
	is := is.New(t)

	expectedFlags := []struct {
		longName   string
		shortName  string
		usage      string
		persistent bool
	}{
		{longName: "version", shortName: "v", usage: "show current Conduit version"},
	}

	e := ecdysis.New()
	c := e.MustBuildCobraCommand(&RootCommand{})

	persistentFlags := c.PersistentFlags()
	cmdFlags := c.Flags()

	for _, f := range expectedFlags {
		var cf *pflag.Flag

		if f.persistent {
			cf = persistentFlags.Lookup(f.longName)
		} else {
			cf = cmdFlags.Lookup(f.longName)
		}
		is.True(cf != nil)
		is.Equal(f.longName, cf.Name)
		is.Equal(f.shortName, cf.Shorthand)
		is.Equal(cf.Usage, f.usage)
	}
}
