package cli

import "github.com/spf13/cobra"

// caskDirFlags are brew's global cask options: target directories and
// --language, which take a value.
var caskDirFlags = []string{
	"appdir", "appimagedir", "keyboard-layoutdir", "colorpickerdir", "prefpanedir", "qlplugindir",
	"mdimporterdir", "dictionarydir", "fontdir", "servicedir", "input-methoddir", "internet-plugindir",
	"audio-unit-plugindir", "vst-plugindir", "vst3-plugindir", "screen-saverdir", "language",
}

// caskSwitches are each command's cask-only switches in brew.
var caskSwitches = map[string][]string{
	"install":   {"binaries", "no-binaries", "require-sha", "adopt", "skip-cask-deps", "zap"},
	"reinstall": {"binaries", "no-binaries", "require-sha", "adopt", "skip-cask-deps", "zap"},
	"upgrade":   {"skip-cask-deps", "no-quit", "greedy", "greedy-latest", "greedy-auto-updates", "binaries", "no-binaries", "require-sha"},
	"uninstall": {"zap"},
}

// addCaskFlags accepts brew's options that only affect casks or HEAD
// installs. gobrew has neither, so like brew given only formulae it ignores
// them, and like brew it rejects a cask option together with --formula. They
// are hidden because they never change what gobrew does.
func addCaskFlags(cmd *cobra.Command) {
	name := cmd.Name()
	flags := cmd.Flags()
	exclusive := caskSwitches[name]
	switch name {
	case "install", "reinstall", "upgrade":
		for _, dir := range caskDirFlags {
			flags.String(dir, "", "Ignored: gobrew has no casks")
		}
		exclusive = append(exclusive, caskDirFlags...)
	}
	for _, s := range caskSwitches[name] {
		if s == "greedy" {
			flags.BoolP(s, "g", false, "Ignored: gobrew has no casks")
		} else {
			flags.Bool(s, false, "Ignored: gobrew has no casks")
		}
	}
	for _, s := range exclusive {
		_ = flags.MarkHidden(s)
		if flags.Lookup("formula") != nil {
			cmd.MarkFlagsMutuallyExclusive("formula", s)
		}
	}
	switch name {
	case "install":
		cmd.MarkFlagsMutuallyExclusive("adopt", "force")
	case "outdated":
		// brew outdated's --greedy options do not conflict with --formula.
		flags.BoolP("greedy", "g", false, "Ignored: gobrew has no casks")
		flags.Bool("greedy-latest", false, "Ignored: gobrew has no casks")
		flags.Bool("greedy-auto-updates", false, "Ignored: gobrew has no casks")
		for _, s := range []string{"greedy", "greedy-latest", "greedy-auto-updates"} {
			_ = flags.MarkHidden(s)
		}
	}
	switch name {
	case "install", "upgrade", "outdated":
		flags.Bool("fetch-HEAD", false, "Ignored: gobrew has no HEAD installs")
		_ = flags.MarkHidden("fetch-HEAD")
	}
}
