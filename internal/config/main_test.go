package config

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points HOME and XDG_CONFIG_HOME at empty directories so no test
// merges the developer's real global config. Tests needing a global tier
// still set their own via isolateHome / t.Setenv.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "sakusen-config-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	xdg, err := os.MkdirTemp("", "sakusen-config-xdg-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", xdg)
	code := m.Run()
	os.RemoveAll(home)
	os.RemoveAll(xdg)
	os.Exit(code)
}
