package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/shqingda/gobrew/internal/model"
)

var formulaName = regexp.MustCompile(`^[a-z0-9][a-z0-9+_.@-]*$`)
var macOSNames = map[string]int{"big_sur": 11, "monterey": 12, "ventura": 13, "sonoma": 14, "sequoia": 15, "tahoe": 26, "golden_gate": 27}

type rawFormula struct {
	Name        string   `json:"name"`
	Tap         string   `json:"tap"`
	Aliases     []string `json:"aliases"`
	Description string   `json:"desc"`
	Homepage    string   `json:"homepage"`
	License     string   `json:"license"`
	Versions    struct {
		Stable string `json:"stable"`
	} `json:"versions"`
	Revision             int  `json:"revision"`
	VersionScheme        int  `json:"version_scheme"`
	CompatibilityVersion *int `json:"compatibility_version"`
	Bottle               struct {
		Stable struct {
			Rebuild int                     `json:"rebuild"`
			Files   map[string]model.Bottle `json:"files"`
		} `json:"stable"`
	} `json:"bottle"`
	Dependencies          []string            `json:"dependencies"`
	NoLinkageDependencies []string            `json:"no_linkage_dependencies"`
	Recommended           []string            `json:"recommended_dependencies"`
	UsesFromMacOS         []json.RawMessage   `json:"uses_from_macos"`
	UsesFromMacOSBounds   []map[string]string `json:"uses_from_macos_bounds"`
	Requirements          []struct {
		Name     string          `json:"name"`
		Version  json.RawMessage `json:"version"`
		Contexts []string        `json:"contexts"`
		Specs    []string        `json:"specs"`
	} `json:"requirements"`
	Conflicts          []string          `json:"conflicts_with"`
	KegOnly            bool              `json:"keg_only"`
	Disabled           bool              `json:"disabled"`
	Deprecated         bool              `json:"deprecated"`
	Caveats            string            `json:"caveats"`
	PostInstallDefined bool              `json:"post_install_defined"`
	Steps              []json.RawMessage `json:"post_install_steps"`
	PourOnlyIf         json.RawMessage   `json:"pour_bottle_only_if"`
}

func normalize(data json.RawMessage, host model.Platform) (model.Formula, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return model.Formula{}, err
	}
	var variations map[string]map[string]json.RawMessage
	if err := json.Unmarshal(fields["variations"], &variations); err != nil && len(fields["variations"]) > 0 {
		return model.Formula{}, err
	}
	tag := ""
	for name, major := range macOSNames {
		if host.Major == major {
			tag = name
			break
		}
	}
	if host.Arch == "arm64" && tag != "" {
		tag = "arm64_" + tag
	}
	// Homebrew applies a shallow merge for the exact host tag. Array-valued
	// dependency sets are replacements, never unions with the default platform.
	for key, value := range variations[tag] {
		fields[key] = value
	}
	delete(fields, "variations")
	merged, err := json.Marshal(fields)
	if err != nil {
		return model.Formula{}, err
	}
	var raw rawFormula
	if err = json.Unmarshal(merged, &raw); err != nil {
		return model.Formula{}, fmt.Errorf("decode formula: %w", err)
	}
	if !formulaName.MatchString(raw.Name) {
		return model.Formula{}, fmt.Errorf("unsafe formula name %q", raw.Name)
	}
	f := model.Formula{Name: raw.Name, Aliases: raw.Aliases, Description: raw.Description, Homepage: raw.Homepage, License: raw.License, Version: raw.Versions.Stable, Revision: raw.Revision, VersionScheme: raw.VersionScheme, CompatibilityVersion: raw.CompatibilityVersion, BottleRebuild: raw.Bottle.Stable.Rebuild, Bottles: raw.Bottle.Stable.Files, Dependencies: append(raw.Dependencies, raw.Recommended...), NoLinkageDependencies: raw.NoLinkageDependencies, Conflicts: raw.Conflicts, KegOnly: raw.KegOnly, Disabled: raw.Disabled, Deprecated: raw.Deprecated, Caveats: raw.Caveats, PostInstallDefined: raw.PostInstallDefined, Steps: raw.Steps}
	unsupported := func(reason string) {
		if f.UnsupportedReason == "" {
			f.UnsupportedReason = reason
		}
	}
	if raw.Tap != "" && raw.Tap != "homebrew/core" {
		unsupported("only official homebrew/core formulae are supported")
	}
	if f.Version == "" || strings.ContainsAny(f.Version, "/\\\x00") || f.Version == "." || f.Version == ".." {
		unsupported("missing or unsafe stable version")
	}
	if f.Revision < 0 || f.VersionScheme < 0 || f.BottleRebuild < 0 || (f.CompatibilityVersion != nil && *f.CompatibilityVersion < 0) {
		unsupported("invalid version metadata")
	}
	if raw.PostInstallDefined {
		unsupported("legacy Ruby post_install is unsupported")
	}
	if len(raw.PourOnlyIf) > 0 && !bytes.Equal(raw.PourOnlyIf, []byte("null")) {
		var condition string
		if json.Unmarshal(raw.PourOnlyIf, &condition) != nil {
			unsupported("unknown bottle pour condition")
		} else {
			switch condition {
			case "default_prefix", ":default_prefix":
				unsupported("bottle requires Homebrew's default prefix")
			default:
				unsupported("unsupported bottle pour condition: " + condition)
			}
		}
	}
	if len(raw.UsesFromMacOSBounds) > 0 && len(raw.UsesFromMacOSBounds) != len(raw.UsesFromMacOS) {
		unsupported("uses_from_macos bounds do not match dependencies")
	}
	for i, dep := range raw.UsesFromMacOS {
		name, contexts, err := macOSDependency(dep)
		if err != nil {
			unsupported(err.Error())
			continue
		}
		if slices.Contains(contexts, "build") || slices.Contains(contexts, "test") {
			continue
		}
		for _, context := range contexts {
			if context != "recommended" && context != "no_linkage" && context != "optional" {
				unsupported("unknown uses_from_macos context: " + context)
			}
		}
		if slices.Contains(contexts, "optional") {
			continue
		}
		useSystem := host.OS == "darwin"
		if i < len(raw.UsesFromMacOSBounds) {
			for key, value := range raw.UsesFromMacOSBounds[i] {
				if key != "since" {
					unsupported("unknown uses_from_macos bound: " + key)
					continue
				}
				major, ok := macOSNames[value]
				if !ok {
					unsupported("unknown macOS dependency version: " + value)
				} else if host.Major < major {
					useSystem = false
				}
			}
		}
		if !useSystem {
			f.Dependencies = append(f.Dependencies, name)
		}
	}
	for _, r := range raw.Requirements {
		for _, spec := range r.Specs {
			if spec != "stable" && spec != "head" {
				unsupported("unknown requirement spec: " + spec)
			}
		}
		if len(r.Specs) > 0 && !slices.Contains(r.Specs, "stable") {
			continue
		}
		if slices.Contains(r.Contexts, "build") || slices.Contains(r.Contexts, "test") {
			continue
		}
		for _, context := range r.Contexts {
			if context != "recommended" {
				unsupported("unsupported runtime requirement context: " + context)
			}
		}
		var version string
		if len(r.Version) > 0 {
			if err := json.Unmarshal(r.Version, &version); err != nil {
				unsupported("invalid runtime requirement version: " + r.Name)
				continue
			}
		}
		switch r.Name {
		case "macos", "maximum_macos":
			if host.OS != "darwin" {
				unsupported("macOS is required")
			}
			if version != "" {
				major, ok := macOSNames[version]
				if !ok {
					major, err = strconv.Atoi(version)
					ok = err == nil
				}
				if !ok {
					unsupported("unknown macOS requirement: " + version)
				} else if r.Name == "maximum_macos" && host.Major > major {
					unsupported("requires macOS at most " + version)
				} else if r.Name == "macos" && host.Major < major {
					unsupported("requires macOS " + version)
				}
			}
		case "arch":
			if version != "arm64" && version != "x86_64" {
				unsupported("unknown architecture requirement: " + version)
			} else if version != host.Arch {
				unsupported("requires architecture " + version)
			}
		case "linux":
			unsupported("requires Linux")
		default:
			unsupported("unsupported runtime requirement: " + r.Name)
		}
	}
	for _, names := range [][]string{f.Dependencies, f.Aliases, f.Conflicts} {
		for _, name := range names {
			if !formulaName.MatchString(strings.TrimPrefix(name, "homebrew/core/")) {
				unsupported("unsupported package reference: " + name)
			}
		}
	}
	sort.Strings(f.Dependencies)
	f.Dependencies = slices.Compact(f.Dependencies)
	for tag, b := range f.Bottles {
		b.Tag = tag
		f.Bottles[tag] = b
	}
	return f, nil
}

func macOSDependency(raw json.RawMessage) (string, []string, error) {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return name, nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || len(object) != 1 {
		return "", nil, fmt.Errorf("invalid uses_from_macos dependency")
	}
	for name, value := range object {
		var contexts []string
		if json.Unmarshal(value, &contexts) == nil {
			return name, contexts, nil
		}
		var context string
		if json.Unmarshal(value, &context) == nil {
			return name, []string{context}, nil
		}
	}
	return "", nil, fmt.Errorf("unknown uses_from_macos dependency context")
}
