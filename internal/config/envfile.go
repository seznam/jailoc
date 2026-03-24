package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ParseEnvFile reads a Docker-style .env file and returns KEY=VALUE pairs.
// Comments, empty lines, and bare keys (no = sign) are skipped.
// Quoted values have their outer quotes stripped. Inline comments are removed.
func ParseEnvFile(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from validated config, not user input
	if err != nil {
		return nil, fmt.Errorf("reading env file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var result []string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		rawKey, rawValue, hasEquals := strings.Cut(trimmed, "=")
		if !hasEquals {
			continue
		}

		key := strings.TrimSpace(rawKey)
		value := parseValue(rawValue)

		result = append(result, key+"="+value)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading env file %s: %w", path, err)
	}

	return result, nil
}

// parseValue handles Docker .env value parsing: quote stripping and inline comment removal.
func parseValue(raw string) string {
	s := strings.TrimSpace(raw)

	if len(s) == 0 {
		return ""
	}

	if s[0] == '"' {
		if closing := strings.Index(s[1:], "\""); closing >= 0 {
			return s[1 : 1+closing]
		}
		return s[1:]
	}

	if s[0] == '\'' {
		if closing := strings.Index(s[1:], "'"); closing >= 0 {
			return s[1 : 1+closing]
		}
		return s[1:]
	}

	if idx := strings.Index(s, " #"); idx >= 0 {
		s = s[:idx]
	}

	return strings.TrimRight(s, " \t")
}
