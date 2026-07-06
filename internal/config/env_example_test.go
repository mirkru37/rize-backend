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

// optionalInExample lists EnvVarNames entries that config.Load reads but
// which .env.example must document only as a commented-out placeholder, not
// an active KEY=value line. JWT_SIGNING_KEY is here because it must stay
// unset by default in a freshly copied .env — that's what routes local dev
// onto the ephemeral in-memory signing key fallback (see config.go /
// documentation/security.md §Token model); an active placeholder value
// would instead be fed to LoadSigningKey and fail to parse as PEM.
var optionalInExample = map[string]bool{
	"JWT_SIGNING_KEY": true,
}

// TestEnvExampleListsAllConfigVars enforces that .env.example documents
// every environment variable config.Load reads (RIZ-54), and that the
// documentation doesn't drift stale in either direction:
//
//   - Every name in EnvVarNames must appear as an ACTIVE ("KEY=value") entry,
//     unless it's in optionalInExample, in which case a commented-out entry
//     is required instead (active would defeat its documented default).
//   - Every ACTIVE entry in .env.example must correspond to a name in
//     EnvVarNames, catching stale entries left behind after a config var is
//     removed from Load. Commented-out entries are exempt from this check
//     since they may be forward-looking placeholders (e.g. SENTRY_DSN for
//     RIZ-53, not yet read by Load).
func TestEnvExampleListsAllConfigVars(t *testing.T) {
	active, commented, err := parseEnvExampleKeys(envExamplePath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", envExamplePath, err)
	}

	for _, name := range EnvVarNames {
		if optionalInExample[name] {
			if !commented[name] {
				t.Errorf("%s must document %q as a commented-out placeholder (see optionalInExample)", envExamplePath, name)
			}
			if active[name] {
				t.Errorf("%s must NOT set %q as an active value — it must stay unset by default (see optionalInExample)", envExamplePath, name)
			}
			continue
		}
		if !active[name] {
			t.Errorf("%s is missing an active %q entry, which config.Load reads", envExamplePath, name)
		}
	}

	envVarNames := make(map[string]bool, len(EnvVarNames))
	for _, name := range EnvVarNames {
		envVarNames[name] = true
	}
	for name := range active {
		if !envVarNames[name] {
			t.Errorf("%s declares active %q, which is not in config.EnvVarNames (stale entry?)", envExamplePath, name)
		}
	}
}

// parseEnvExampleKeys reads a dotenv-style file and returns two sets of
// variable names it declares: active ("KEY=value") and commented-out
// ("# KEY=value"). Decorative comment lines (section headers, prose) are
// ignored because their leading token before "=" does not look like an env
// var name.
func parseEnvExampleKeys(path string) (active map[string]bool, commented map[string]bool, err error) {
	f, err := os.Open(path) //nolint:gosec // path is a fixed test constant, not user input
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()

	active = make(map[string]bool)
	commented = make(map[string]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		isCommented := strings.HasPrefix(line, "#")
		line = strings.TrimPrefix(line, "#")
		line = strings.TrimSpace(line)

		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if !envKeyPattern.MatchString(key) {
			continue
		}
		if isCommented {
			commented[key] = true
		} else {
			active[key] = true
		}
	}
	return active, commented, scanner.Err()
}
