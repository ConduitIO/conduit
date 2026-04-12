package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Masterminds/semver/v3"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <current_version>\n", os.Args[0])
		os.Exit(1)
	}
	currentVersionStr := os.Args[1]

	// Ensure version starts with 'v' for semver parsing
	if !strings.HasPrefix(currentVersionStr, "v") {
		currentVersionStr = "v" + currentVersionStr
	}

	v, err := semver.NewVersion(currentVersionStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing version '%s': %v\n", currentVersionStr, err)
		os.Exit(1)
	}

	// Increment the minor version and reset patch to 0
	nextMinor := v.IncMinor()
	developVersion := fmt.Sprintf("v%d.%d.0-develop", nextMinor.Major(), nextMinor.Minor()) // Reset patch to 0 for minor increment

	fmt.Print(developVersion)
}
