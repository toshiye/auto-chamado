// Package main implements a structural context compiler for the AutoChamados project.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Global exclusion list configured specifically for the AutoChamados Ecosystem architecture
var ignoreList = []string{
	".git",
	".go_tmp",            // Temporary Go build outputs
	"processed.json",     // Local state tracking database
	"config.json",        // Local environment configuration and API secrets
	"context_output.txt", // Avoid infinite recursion
	"context_builder.go", // Ignore the script itself
}

func main() {
	outputFile := "context_output.txt"

	out, err := os.Create(outputFile)
	if err != nil {
		fmt.Printf("Error creating context repository output file: %v\n", err)
		return
	}
	defer out.Close()

	fmt.Println("🔍 Compiling AutoChamados Project Structural Blueprint...")

	// 1. Generate visual folder structure map
	out.WriteString("==================================================\n")
	out.WriteString("AUTOCHAMADOS PROJECT ARCHITECTURE STRUCTURE MAP\n")
	out.WriteString("==================================================\n\n")

	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if shouldIgnore(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		depth := strings.Count(filepath.ToSlash(path), "/")
		indent := strings.Repeat("  ", depth)
		out.WriteString(fmt.Sprintf("%s|- %s\n", indent, d.Name()))
		return nil
	})

	out.WriteString("\n==================================================\n")
	out.WriteString("SOURCE LOGIC & CONFIGURATION FILES CONTENT STREAM\n")
	out.WriteString("==================================================\n\n")

	// 2. Scan and stream valid file content blocks
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if shouldIgnore(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.IsDir() {
			ext := strings.ToLower(filepath.Ext(path))
			// Filter for programming logic, configuration maps, and manifests
			if ext == ".go" || ext == ".mod" || ext == ".work" || ext == ".json" || ext == ".yaml" || ext == ".md" {
				fmt.Printf("📦 Streaming file chunk: %s\n", path)

				out.WriteString(fmt.Sprintf("--- START OF FILE REFERENCE: %s ---\n", path))

				content, err := os.ReadFile(path)
				if err != nil {
					out.WriteString(fmt.Sprintf("// File system error reading chunk: %v\n", err))
				} else {
					out.WriteString(string(content))
				}

				out.WriteString(fmt.Sprintf("\n--- END OF FILE REFERENCE: %s ---\n\n", path))
			}
		}
		return nil
	})

	if err != nil {
		fmt.Printf("Fatal error generating operational context package: %v\n", err)
		return
	}

	fmt.Printf("\n✨ Operational Context Assembled! Target path: %s\n", outputFile)
}

// shouldIgnore normalizes paths to filter undesirable operational artifacts
func shouldIgnore(path string) bool {
	normalizedPath := filepath.ToSlash(path)

	if strings.HasSuffix(strings.ToLower(normalizedPath), ".exe") {
		return true
	}

	for _, ignore := range ignoreList {
		if strings.Contains(normalizedPath, ignore) {
			return true
		}
	}
	return false
}
