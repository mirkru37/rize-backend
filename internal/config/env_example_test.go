package config

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

// envExamplePath is the location of the documented environment variable
// list, relative to this package's directory.
const envExamplePath = "../../.env.example"

// envKeyPattern matches a dotenv variable name (e.g. DATABASE_URL). It is
// used to distinguish real KEY=... declarations (including commented-out
// placeholders like "# SENTRY_DSN=...") from decorative comment lines such
// as section headers, which never look like an all-caps identifier.
var envKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// TestEnvExampleListsAllConfigVars enforces that .env.example documents
// every environment variable config.Load reads (RIZ-54). If a future change
// adds a new env var to Load without adding a matching entry to
// .env.example, this test fails, catching drift between the two at PR time
// rather than at a deploy-time surprise.
func TestEnvExampleListsAllConfigVars(t *testing.T) {
	documented, err := parseEnvExampleKeys(envExamplePath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", envExamplePath, err)
	}

	for _, name := range EnvVarNames {
		if !documented[name] {
			t.Errorf("%s is missing %q, which config.Load reads", envExamplePath, name)
		}
	}
}

// parseEnvExampleKeys reads a dotenv-style file and returns the set of
// variable names it declares. Both active ("KEY=value") and commented-out
// ("# KEY=value") declarations count, so a documented-but-not-yet-active
// placeholder (e.g. a future SENTRY_DSN entry) is still recognized.
// Decorative comment lines (section headers, prose) are ignored because
// their leading token before "=" does not look like an env var name.
func parseEnvExampleKeys(path string) (map[string]bool, error) {
	f, err := os.Open(path) //nolint:gosec // path is a fixed test constant, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	keys := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "#")
		line = strings.TrimSpace(line)

		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if envKeyPattern.MatchString(key) {
			keys[key] = true
		}
	}
	return keys, scanner.Err()
}
