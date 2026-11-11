package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// environmentHelp is `gobrew help environment`, which the "see `gobrew help`"
// hints point to. Values are set when non-empty unless a default is given.
const environmentHelp = `Environment variables read by gobrew:

  GOBREW_PREFIX                      Installation prefix (default /opt/gobrew); --prefix wins
  GOBREW_NO_AUTO_UPDATE              Do not refresh the catalog or gobrew before install-type commands
  GOBREW_AUTO_UPDATE_SECS            Seconds between automatic refreshes (default 86400)
  GOBREW_NO_INSTALL_UPGRADE          Do not upgrade already installed formulae named to install
  GOBREW_NO_INSTALL_CLEANUP          Do not clean up after install, upgrade and reinstall
  GOBREW_NO_CLEANUP_FORMULAE         Comma-separated formulae never cleaned up
  GOBREW_CLEANUP_MAX_AGE_DAYS        Prune cache entries unused for longer (default 120)
  GOBREW_CLEANUP_PERIODIC_FULL_DAYS  Days between automatic full cleanups (default 30)
  GOBREW_DISPLAY_INSTALL_TIMES       Print install times for each package, like --display-times
  GOBREW_NO_MODIFY_PATH              Do not add gobrew to shell startup files on first install
  GOBREW_NO_PATH_SHADOW_CHECK        Do not warn when other commands shadow installed ones
  GOBREW_NO_ENV_HINTS                Hide hints about these variables
  GOBREW_NO_EMOJI                    Print words instead of ✔ and ✘
  GOBREW_NO_COLOR                    Disable color (NO_COLOR and HOMEBREW_NO_COLOR work too)
  GOBREW_NO_ANALYTICS                Do not fetch install counts for info
  GOBREW_NO_GITHUB_API               Do not call the GitHub API (also disables install counts)
  GOBREW_INSTALL_BIN_DIR             Used by install.sh: install the gobrew CLI into this directory
  HOMEBREW_GITHUB_API_TOKEN          GitHub token for API requests (GITHUB_TOKEN works too)
  HOMEBREW_PREFIX                    Homebrew's prefix for PATH advice (default /opt/homebrew)
`

// helpTopics are `gobrew help <topic>` pages that are not commands.
var helpTopics = map[string]string{"environment": environmentHelp, "env": environmentHelp}

// examples are shown in each command's help, as in brew's manual.
var examples = map[string]string{
	"install":   "  gobrew install jq\n  gobrew install --dry-run ffmpeg\n  gobrew install --only-dependencies wget",
	"upgrade":   "  gobrew upgrade\n  gobrew upgrade jq --dry-run",
	"reinstall": "  gobrew reinstall jq",
	"uninstall": "  gobrew uninstall jq",
	"search":    "  gobrew search jq\n  gobrew search --desc \"json processor\"\n  gobrew search /^lib.*ssl$/",
	"info":      "  gobrew info jq\n  gobrew info --installed --json",
	"list":      "  gobrew list\n  gobrew list --versions\n  gobrew list jq",
	"deps":      "  gobrew deps --tree ffmpeg\n  gobrew deps --installed",
	"uses":      "  gobrew uses --installed openssl@3",
	"outdated":  "  gobrew outdated\n  gobrew outdated --verbose",
	"update":    "  gobrew update",
	"cleanup":   "  gobrew cleanup\n  gobrew cleanup --dry-run\n  gobrew cleanup --prune=all",
	"doctor":    "  gobrew doctor\n  gobrew doctor --repair",
	"link":      "  gobrew link jq\n  gobrew link --force --dry-run openssl@3",
	"pin":       "  gobrew pin node\n  gobrew upgrade   # leaves node as it is",
	"self":      "  gobrew self update\n  gobrew self uninstall",
}

// addHelpText adds examples and longer descriptions to the built commands.
func addHelpText(root *cobra.Command) {
	root.Long += "\n\nLike brew, `gobrew --prefix`, `--cellar` and `--cache` print gobrew's paths.\n" +
		"Run `gobrew help environment` for the environment variables gobrew reads."
	for _, command := range root.Commands() {
		if example, ok := examples[command.Name()]; ok {
			command.Example = example
		}
		if command.Name() == "uninstall" {
			command.Long = "Remove packages while retaining configuration and data.\n" +
				"Dependencies that no remaining requested or pinned formula needs\n" +
				"are removed too, as `gobrew autoremove` would."
		}
	}
}

// helpTopic returns the text of a non-command help topic.
func helpTopic(args []string) (string, bool) {
	if len(args) != 1 {
		return "", false
	}
	text, ok := helpTopics[strings.ToLower(args[0])]
	return text, ok
}
