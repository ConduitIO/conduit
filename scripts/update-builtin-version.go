// scripts/update-builtin-version.go
package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
)

const (
	versionFilePath = "./pkg/conduit/version.go"
	constantName    = "BuiltinConnectorsVersion"
)

var (
	// regex to find the BuiltinConnectorsVersion constant
	versionConstRegex = regexp.MustCompile(fmt.Sprintf(`(?P<prefix>const\s+%s\s*=\s*")(?P<version>v\d+\.\d+\.\d+(-develop)?)(?P<suffix>")`, constantName))
)

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage:\n")
		fmt.Printf("  %s release <tag>\n", os.Args[0])
		fmt.Printf("  %s develop\n", os.Args[0])
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "release":
		if len(os.Args) < 3 {
			fmt.Printf("Error: 'release' command requires a tag argument.\n")
			os.Exit(1)
		}
		tag := os.Args[2]
		if err := validateReleaseVersion(tag); err != nil {
			fmt.Printf("Validation failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("BuiltinConnectorsVersion successfully validated against tag %s.\n", tag)
	case "develop":
		if err := updateDevelopVersion(); err != nil {
			fmt.Printf("Error updating develop version: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("BuiltinConnectorsVersion successfully updated to next develop version.\n")
	default:
		fmt.Printf("Error: Unknown command '%s'. Use 'release' or 'develop'.\n", command)
		os.Exit(1)
	}
}

func readVersionFile() ([]byte, error) {
	return ioutil.ReadFile(versionFilePath)
}

func writeVersionFile(content []byte) error {
	return ioutil.WriteFile(versionFilePath, content, 0644)
}

func extractCurrentVersion(fileContent []byte) (string, error) {
	matches := versionConstRegex.FindStringSubmatch(string(fileContent))
	if len(matches) < 4 { // 0: full match, 1: prefix, 2: version, 3: suffix
		return "", fmt.Errorf("could not find constant %s in %s", constantName, versionFilePath)
	}
	return matches[2], nil
}

func validateReleaseVersion(expectedTag string) error {
	fileContent, err := readVersionFile()
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", versionFilePath, err)
	}

	currentVersionStr, err := extractCurrentVersion(fileContent)
	if err != nil {
		return err
	}

	// Remove 'v' prefix from tag for semver parsing if present, then add it back for comparison
	if strings.HasPrefix(expectedTag, "v") {
		expectedTag = expectedTag[1:]
	}

	currentSemVer, err := semver.NewVersion(strings.TrimPrefix(currentVersionStr, "v"))
	if err != nil {
		return fmt.Errorf("invalid current semver format '%s': %w", currentVersionStr, err)
	}

	expectedSemVer, err := semver.NewVersion(expectedTag)
	if err != nil {
		return fmt.Errorf("invalid tag semver format '%s': %w", expectedTag, err)
	}

	// The BuiltinConnectorsVersion should exactly match the release tag, without any -develop suffix.
	// So, we compare the core version strings.
	if currentSemVer.String() != expectedSemVer.String() {
		return fmt.Errorf("version mismatch: %s (in file) != v%s (from tag)", currentVersionStr, expectedSemVer.String())
	}

	// Ensure there's no -develop suffix in the constant for a release build
	if strings.Contains(currentVersionStr, "-develop") {
		return fmt.Errorf("BuiltinConnectorsVersion '%s' contains '-develop' suffix, but is being validated for a release tag '%s'", currentVersionStr, "v"+expectedSemVer.String())
	}

	return nil
}

func updateDevelopVersion() error {
	fileContent, err := readVersionFile()
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", versionFilePath, err)
	}

	currentVersionStr, err := extractCurrentVersion(fileContent)
	if err != nil {
		return err
	}

	// Remove 'v' prefix and '-develop' suffix for semver parsing
	sanitizedVersionStr := strings.TrimPrefix(currentVersionStr, "v")
	sanitizedVersionStr = strings.TrimSuffix(sanitizedVersionStr, "-develop")

	currentSemVer, err := semver.NewVersion(sanitizedVersionStr)
	if err != nil {
		return fmt.Errorf("invalid current semver format '%s': %w", currentVersionStr, err)
	}

	// Increment minor version (e.g., v0.1.0 -> v0.2.0)
	nextMinorVersion := currentSemVer.IncMinor()
	newVersionStr := fmt.Sprintf("v%s-develop", nextMinorVersion.String())

	// Replace the old version string with the new one
	newFileContent := versionConstRegex.ReplaceAllString(string(fileContent),
		fmt.Sprintf("${1}%s${3}", newVersionStr))

	if err := writeVersionFile([]byte(newFileContent)); err != nil {
		return fmt.Errorf("failed to write %s: %w", versionFilePath, err)
	}

	fmt.Printf("Updated %s from %s to %s\n", constantName, currentVersionStr, newVersionStr)
	return nil
}
