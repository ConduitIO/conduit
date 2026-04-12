package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	targetDir    = "pkg/plugin/connector/builtin/impl"
	targetFile   = "spec.go"
	versionConst = "const Version = "
)

func main() {
	err := filepath.WalkDir(targetDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == targetFile {
			content, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("could not read file %s: %w", path, err)
			}
			if strings.Contains(string(content), versionConst) {
				fmt.Println(path)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding connector specs: %v\n", err)
		os.Exit(1)
	}
}
