package config

import (
	"fmt"
	"os"
	"strings"
)

var secretEnvironmentKeys = map[string]bool{
	"MTS_P115_REFRESH_TOKEN": true,
	"MTS_ARIA2_RPC_SECRET":   true,
}

func LoadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, rawLine := range splitLines(string(data)) {
		key, value, ok := parseLine(rawLine)
		if !ok {
			continue
		}
		if !secretEnvironmentKeys[key] {
			return fmt.Errorf(
				"%s 只能包含秘密环境变量，%s 应移到 config.toml 或删除",
				path, key,
			)
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func splitLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return strings.Split(content, "\n")
}

func parseLine(rawLine string) (string, string, bool) {
	line := strings.TrimSpace(rawLine)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') ||
		(value[0] == '\'' && value[len(value)-1] == '\'')) {
		value = value[1 : len(value)-1]
	}
	return key, value, true
}
