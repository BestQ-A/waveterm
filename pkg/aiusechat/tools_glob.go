// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wavetermdev/waveterm/pkg/aiusechat/uctypes"
	"github.com/wavetermdev/waveterm/pkg/util/utilfn"
	"github.com/wavetermdev/waveterm/pkg/wavebase"
)

const GlobDefaultMaxResults = 100
const GlobHardMaxResults = 10000

// globIgnoreDirs are directories that should always be skipped during glob walks.
var globIgnoreDirs = map[string]bool{
	".git":          true,
	"node_modules":  true,
	".next":         true,
	"dist":          true,
	"vendor":        true,
	"__pycache__":   true,
	".venv":         true,
	"venv":          true,
	".tox":          true,
	"build":         true,
	".cache":        true,
	".idea":         true,
	".vscode":       true,
}

type globParams struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	MaxResults *int   `json:"max_results"`
}

type globFileEntry struct {
	path    string
	modTime int64
}

func parseGlobInput(input any) (*globParams, error) {
	result := &globParams{}

	if input == nil {
		return nil, fmt.Errorf("input is required")
	}

	if err := utilfn.ReUnmarshal(result, input); err != nil {
		return nil, fmt.Errorf("invalid input format: %w", err)
	}

	if result.Pattern == "" {
		return nil, fmt.Errorf("missing pattern parameter")
	}

	if result.MaxResults == nil {
		maxResults := GlobDefaultMaxResults
		result.MaxResults = &maxResults
	}

	if *result.MaxResults < 1 {
		return nil, fmt.Errorf("max_results must be at least 1, got %d", *result.MaxResults)
	}

	if *result.MaxResults > GlobHardMaxResults {
		return nil, fmt.Errorf("max_results cannot exceed %d, got %d", GlobHardMaxResults, *result.MaxResults)
	}

	return result, nil
}

// matchGlobPattern matches a file path relative to the search root against a glob pattern.
// Supports ** for matching across directory boundaries.
func matchGlobPattern(pattern, relPath string) bool {
	// Normalize separators to forward slash for matching
	relPath = filepath.ToSlash(relPath)
	pattern = filepath.ToSlash(pattern)

	return doublestarMatch(pattern, relPath)
}

// doublestarMatch implements glob matching with ** support.
func doublestarMatch(pattern, name string) bool {
	// Handle empty cases
	if pattern == "" {
		return name == ""
	}

	// Split pattern into segments
	patParts := strings.Split(pattern, "/")
	nameParts := strings.Split(name, "/")

	return matchParts(patParts, nameParts)
}

func matchParts(patParts, nameParts []string) bool {
	for len(patParts) > 0 {
		pat := patParts[0]
		patParts = patParts[1:]

		if pat == "**" {
			// ** matches zero or more path segments
			// Try matching remaining pattern against every suffix of nameParts
			for i := 0; i <= len(nameParts); i++ {
				if matchParts(patParts, nameParts[i:]) {
					return true
				}
			}
			return false
		}

		if len(nameParts) == 0 {
			return false
		}

		matched, err := filepath.Match(pat, nameParts[0])
		if err != nil || !matched {
			return false
		}
		nameParts = nameParts[1:]
	}

	return len(nameParts) == 0
}

func globCallback(input any, toolUseData *uctypes.UIMessageDataToolUse) (any, error) {
	params, err := parseGlobInput(input)
	if err != nil {
		return nil, err
	}

	searchPath := params.Path
	if searchPath == "" {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return nil, fmt.Errorf("failed to get current working directory: %w", cwdErr)
		}
		searchPath = cwd
	}

	expandedPath, err := wavebase.ExpandHomeDir(searchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to expand path: %w", err)
	}

	if !filepath.IsAbs(expandedPath) {
		return nil, fmt.Errorf("path must be absolute, got relative path: %s", searchPath)
	}

	_, statErr := os.Stat(expandedPath)
	if statErr != nil {
		return nil, fmt.Errorf("failed to stat path: %w", statErr)
	}

	var entries []globFileEntry
	truncated := false

	walkErr := filepath.WalkDir(expandedPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable entries rather than failing the whole walk
			return nil
		}

		// Skip ignored directories
		if d.IsDir() {
			if globIgnoreDirs[d.Name()] {
				return filepath.SkipDir
			}
			// Skip hidden directories (starting with .) except the root
			if path != expandedPath && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}

		// Only match files
		relPath, relErr := filepath.Rel(expandedPath, path)
		if relErr != nil {
			return nil
		}

		if !matchGlobPattern(params.Pattern, relPath) {
			return nil
		}

		// Enforce hard limit during walk to avoid OOM on huge trees
		if len(entries) >= *params.MaxResults {
			truncated = true
			return filepath.SkipAll
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}

		entries = append(entries, globFileEntry{
			path:    path,
			modTime: info.ModTime().UnixMilli(),
		})
		return nil
	})

	if walkErr != nil {
		return nil, fmt.Errorf("error walking directory: %w", walkErr)
	}

	// Sort by modification time descending (most recently modified first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].modTime > entries[j].modTime
	})

	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = filepath.ToSlash(e.path)
	}

	resultMap := map[string]any{
		"pattern":      params.Pattern,
		"path":         expandedPath,
		"result_count": len(paths),
		"files":        paths,
	}

	if truncated {
		resultMap["truncated"] = true
		resultMap["truncated_message"] = fmt.Sprintf("Results truncated to %d files. Use a more specific pattern or increase max_results.", *params.MaxResults)
	}

	return resultMap, nil
}

func GetGlobToolDefinition() uctypes.ToolDefinition {
	return uctypes.ToolDefinition{
		Name:        "glob",
		DisplayName: "Glob Files",
		Description: "Search for files matching a glob pattern. Supports ** for recursive matching (e.g. **/*.go). Returns matching file paths sorted by modification time (most recent first). Ignores common non-source directories like .git, node_modules, dist, vendor, __pycache__.",
		ToolLogName: "gen:glob",
		Strict:      false,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern to match files against. Supports * (single segment wildcard) and ** (multi-segment wildcard). Examples: '**/*.go', 'src/**/*.ts', '*.json'.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to the root directory to search from. Supports '~' for the user's home directory. Defaults to the current working directory if not specified.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     10000,
					"default":     100,
					"description": "Maximum number of files to return. Defaults to 100, max 10000.",
				},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
		ToolCallDesc: func(input any, output any, toolUseData *uctypes.UIMessageDataToolUse) string {
			parsed, err := parseGlobInput(input)
			if err != nil {
				return fmt.Sprintf("error parsing input: %v", err)
			}

			searchPath := parsed.Path
			if searchPath == "" {
				searchPath = "(cwd)"
			}

			if output != nil {
				if outputMap, ok := output.(map[string]any); ok {
					if count, ok := outputMap["result_count"].(int); ok {
						return fmt.Sprintf("glob %q in %s (%d files found)", parsed.Pattern, searchPath, count)
					}
				}
			}
			return fmt.Sprintf("glob %q in %s", parsed.Pattern, searchPath)
		},
		ToolAnyCallback: globCallback,
		ToolApproval: func(input any) string {
			return uctypes.ApprovalAutoApproved
		},
	}
}
