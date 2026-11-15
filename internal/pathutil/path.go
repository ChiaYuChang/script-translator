package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome resolves a leading "~", "~/", or "~\\" to the current user's
// home directory while leaving empty paths, "-", and non-tilde paths untouched.
func ExpandHome(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" || p == "-" {
		return p, nil
	}
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for %q: %w", raw, err)
		}
		return home, nil
	}
	for _, prefix := range []string{"~/", `~\`} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve home directory for %q: %w", raw, err)
			}
			if rest == "" {
				return home, nil
			}
			if prefix == `~\` {
				rest = filepath.FromSlash(strings.ReplaceAll(rest, `\`, "/"))
			}
			return filepath.Join(home, rest), nil
		}
	}
	return p, nil
}
