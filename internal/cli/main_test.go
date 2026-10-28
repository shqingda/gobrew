package cli

import (
	"os"
	"testing"
)

// TestMain isolates every test from the developer's home directory and shell
// startup files, which installation now edits for PATH setup.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "gobrew-cli-home-")
	if err != nil {
		panic(err)
	}
	for key, value := range map[string]string{"HOME": home, "SHELL": "/bin/zsh", "ZDOTDIR": "", "XDG_CONFIG_HOME": "", "HOMEBREW_PREFIX": home + "/homebrew", "GOBREW_PREFIX": "", "GOBREW_NO_MODIFY_PATH": "", "GOBREW_NO_AUTO_UPDATE": "1", "GOBREW_AUTO_UPDATE_SECS": "", "GOBREW_NO_ENV_HINTS": "", "GOBREW_NO_INSTALL_UPGRADE": ""} {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
