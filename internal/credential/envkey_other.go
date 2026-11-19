//go:build !windows

package credential

import (
	"os"
	"strings"
)

// UserEnvKey reads DEEPSEEK_API_KEY from the process environment. Non-Windows
// builds have no user-environment registry to fall back to.
func UserEnvKey() string {
	return strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
}
