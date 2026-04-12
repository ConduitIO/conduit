package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	versionFile       = "pkg/conduit/version.go"
	versionConstRegex = `(const Current = ")v(\d+)\.(\d+)\.(\d+)(-[\w.-]+)?(".*)`
)

func main() {
	if len(os.Args) < 2 {
		printUsageAndExit()
	}

	command := os.Args[1]
	switch command {
	case "set":
		if len(os.Args) != 3 {
			printUsageAndExit()
		}
		newVersion := os.Args[2]
		if !strings.HasPrefix(newVersion, "v") {
			newVersion = "v" + newVersion
		}
		if err := updateVersion(newVersion); err != nil {
			fmt.Fprintf(os.Stderr, "Error setting version: %v\n", err)
			os.Exit(1)
		}
	case "next-develop":
		if len(os.Args) != 2 {
			printUsageAndExit()
		}
		if err := updateVersionToNextDevelop(); err != nil {
			fmt.Fprintf(os.Stderr, "Error updating to next develop version: %v\n", err)
			os.Exit(1)
		}
	default:
		printUsageAndExit()
	}
}

func printUsageAndExit() {
	fmt.Println("Usage:")
	fmt.Println("  update-version set <version>      - Sets the version constant to the specified version (e.g., v1.2.3)")
	fmt.Println("  update-version next-develop       - Increments the minor version and adds a '-develop' suffix (e.g., v1.2.3 -> v1.3.0-develop)")
	os.Exit(1)
}

func updateVersion(newVersion string) error {
	input, err := os.ReadFile(versionFile)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", versionFile, err)
	}

	re := regexp.MustCompile(versionConstRegex)
	if !re.Match(input) {
		return fmt.Errorf("could not find 'const Current' in %s matching expected pattern", versionFile)
	}

	output := re.ReplaceAllString(string(input), fmt.Sprintf("${1}%s${6}", newVersion))

	if err := os.WriteFile(versionFile, []byte(output), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", versionFile, err)
	}

	fmt.Printf("Successfully updated %s to %s\n", versionFile, newVersion)
	return nil
}

func updateVersionToNextDevelop() error {
	input, err := os.ReadFile(versionFile)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", versionFile, err)
	}

	re := regexp.MustCompile(versionConstRegex)
	matches := re.FindStringSubmatch(string(input))
	if matches == nil {
		return fmt.Errorf("could not find 'const Current' in %s matching expected pattern", versionFile)
	}

	// matches: [full_match, prefix, major, minor, patch, prerelease, suffix]
	major, _ := strconv.Atoi(matches[2])
	minor, _ := strconv.Atoi(matches[3])
	// patch, _ := strconv.Atoi(matches[4]) // Not used for next-develop logic

	// Increment minor version, reset patch to 0, add -develop suffix
	newMinor := minor + 1
	newPatch := 0
	newVersion := fmt.Sprintf("v%d.%d.%d-develop", major, newMinor, newPatch)

	output := re.ReplaceAllString(string(input), fmt.Sprintf("${1}%s${6}", newVersion))

	if err := os.WriteFile(versionFile, []byte(output), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", versionFile, err)
	}

	fmt.Printf("Successfully updated %s to %s (next develop version)\n", versionFile, newVersion)
	return nil
}
