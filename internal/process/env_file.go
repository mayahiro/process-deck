package process

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func readEnvFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open env_file %s: %w", path, err)
	}
	defer file.Close()

	return parseEnvFile(file, path)
}

func parseEnvFile(r io.Reader, source string) ([]string, error) {
	scanner := bufio.NewScanner(r)
	entries := make([]string, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		entry, ok, err := parseEnvFileLine(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", source, lineNumber, err)
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read env_file %s: %w", source, err)
	}
	return entries, nil
}

func parseEnvFileLine(line string) (string, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false, nil
	}

	key, value, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", false, fmt.Errorf("line must use KEY=VALUE syntax")
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, fmt.Errorf("key must not be empty")
	}
	if strings.Contains(key, "=") {
		return "", false, fmt.Errorf("key %q must not contain =", key)
	}

	value, err := parseEnvFileValue(strings.TrimSpace(value))
	if err != nil {
		return "", false, err
	}
	return key + "=" + value, true, nil
}

func parseEnvFileValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] == '"' || value[0] == '\'' {
		return parseQuotedEnvValue(value)
	}
	return strings.TrimSpace(stripEnvInlineComment(value)), nil
}

func parseQuotedEnvValue(value string) (string, error) {
	quote := value[0]
	closing := findClosingQuote(value, quote)
	if closing == -1 {
		return "", fmt.Errorf("quoted value is missing a closing quote")
	}

	suffix := strings.TrimSpace(value[closing+1:])
	if suffix != "" && !strings.HasPrefix(suffix, "#") {
		return "", fmt.Errorf("quoted value has unexpected trailing content")
	}

	quoted := value[:closing+1]
	if quote == '"' {
		unquoted, err := strconv.Unquote(quoted)
		if err != nil {
			return "", err
		}
		return unquoted, nil
	}
	return strings.ReplaceAll(quoted[1:closing], `\'`, `'`), nil
}

func findClosingQuote(value string, quote byte) int {
	escaped := false
	for i := 1; i < len(value); i++ {
		if value[i] == '\\' && !escaped {
			escaped = true
			continue
		}
		if value[i] == quote && !escaped {
			return i
		}
		escaped = false
	}
	return -1
}

func stripEnvInlineComment(value string) string {
	for i := 1; i < len(value); i++ {
		if value[i] == '#' && isEnvSpace(value[i-1]) {
			return value[:i]
		}
	}
	return value
}

func isEnvSpace(ch byte) bool {
	return ch == ' ' || ch == '\t'
}

func resolveEnvFilePath(cwd string, path string) string {
	if filepath.IsAbs(path) || cwd == "" {
		return path
	}
	return filepath.Join(cwd, path)
}
