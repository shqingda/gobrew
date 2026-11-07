package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
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
	URLs struct {
		Stable struct {
			URL   string `json:"url"`
			Using any    `json:"using"`
		} `json:"stable"`
	} `json:"urls"`
	Dependencies          []string            `json:"dependencies"`
	NoLinkageDependencies []string            `json:"no_linkage_dependencies"`
	Build                 []string            `json:"build_dependencies"`
	Test                  []string            `json:"test_dependencies"`
	Recommended           []string            `json:"recommended_dependencies"`
	Optional              []string            `json:"optional_dependencies"`
	UsesFromMacOS         []json.RawMessage   `json:"uses_from_macos"`
	UsesFromMacOSBounds   []map[string]string `json:"uses_from_macos_bounds"`
	Requirements          []struct {
		Name     string          `json:"name"`
		Version  json.RawMessage `json:"version"`
		Contexts []string        `json:"contexts"`
		Specs    []string        `json:"specs"`
	} `json:"requirements"`
	Conflicts       []string `json:"conflicts_with"`
	ConflictReasons []string `json:"conflicts_with_reasons"`
	SourcePath      string   `json:"ruby_source_path"`
	KegOnly         bool     `json:"keg_only"`
	KegOnlyReason   struct {
		Reason      string `json:"reason"`
		Explanation string `json:"explanation"`
	} `json:"keg_only_reason"`
	Disabled           bool              `json:"disabled"`
	Deprecated         bool              `json:"deprecated"`
	DeprecationDate    string            `json:"deprecation_date"`
	DeprecationReason  string            `json:"deprecation_reason"`
	DeprecationFormula string            `json:"deprecation_replacement_formula"`
	DeprecationCask    string            `json:"deprecation_replacement_cask"`
	DisableDate        string            `json:"disable_date"`
	DisableReason      string            `json:"disable_reason"`
	DisableFormula     string            `json:"disable_replacement_formula"`
	DisableCask        string            `json:"disable_replacement_cask"`
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
	f := model.Formula{Name: raw.Name, Aliases: raw.Aliases, Description: raw.Description, Homepage: raw.Homepage, License: raw.License, Version: raw.Versions.Stable, Revision: raw.Revision, VersionScheme: raw.VersionScheme, CompatibilityVersion: raw.CompatibilityVersion, BottleRebuild: raw.Bottle.Stable.Rebuild, Bottles: raw.Bottle.Stable.Files, Dependencies: append(raw.Dependencies, raw.Recommended...), NoLinkageDependencies: raw.NoLinkageDependencies, Conflicts: raw.Conflicts, ConflictReasons: raw.ConflictReasons, SourcePath: raw.SourcePath, KegOnly: raw.KegOnly, KegOnlyReason: raw.KegOnlyReason.Reason, KegOnlyExplanation: raw.KegOnlyReason.Explanation, Disabled: raw.Disabled, Deprecated: raw.Deprecated, DeprecationDate: raw.DeprecationDate, DeprecationReason: raw.DeprecationReason, DeprecationFormula: raw.DeprecationFormula, DeprecationCask: raw.DeprecationCask, DisableDate: raw.DisableDate, DisableReason: raw.DisableReason, DisableFormula: raw.DisableFormula, DisableCask: raw.DisableCask, Caveats: raw.Caveats, PostInstallDefined: raw.PostInstallDefined, Steps: raw.Steps}
	unsupported := func(reason string) {
		if f.UnsupportedReason == "" {
			f.UnsupportedReason = reason
		}
	}
	if raw.Tap != "" && raw.Tap != "homebrew/core" {
		unsupported("only official homebrew/core formulae are supported")
	}
	if !model.ValidPathComponent(f.Version) {
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
	f.DeclaredDependencies = declaredDependencies(raw)
	if len(raw.UsesFromMacOSBounds) > 0 && len(raw.UsesFromMacOSBounds) != len(raw.UsesFromMacOS) {
		unsupported("uses_from_macos bounds do not match dependencies")
	}
	for i, dep := range raw.UsesFromMacOS {
		name, contexts, err := macOSDependency(dep)
		if err != nil {
			unsupported(err.Error())
			continue
		}
		// Only runtime dependencies decide whether a bottle can be installed.
		buildOrTest := slices.Contains(contexts, "build") || slices.Contains(contexts, "test")
		runtime := !buildOrTest && !slices.Contains(contexts, "optional")
		for _, context := range contexts {
			if !buildOrTest && context != "recommended" && context != "no_linkage" && context != "optional" {
				unsupported("unknown uses_from_macos context: " + context)
			}
		}
		useSystem := host.OS == "darwin"
		if i < len(raw.UsesFromMacOSBounds) {
			for key, value := range raw.UsesFromMacOSBounds[i] {
				major, ok := macOSNames[value]
				switch {
				case key != "since":
					if runtime {
						unsupported("unknown uses_from_macos bound: " + key)
					}
				case !ok:
					if runtime {
						unsupported("unknown macOS dependency version: " + value)
					}
				case host.Major < major:
					useSystem = false
				}
			}
		}
		if useSystem {
			continue
		}
		f.DeclaredDependencies = append(f.DeclaredDependencies, model.DeclaredDependency{
			Name: name, Build: slices.Contains(contexts, "build"), Test: slices.Contains(contexts, "test"),
			Recommended: slices.Contains(contexts, "recommended"), Optional: slices.Contains(contexts, "optional"),
			NoLinkage: slices.Contains(contexts, "no_linkage"),
		})
		if runtime {
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
	// brew deps --tree and --topological show declaration order; plans sort
	// their own copy.
	declared := make(map[string]bool, len(f.Dependencies))
	f.Dependencies = slices.DeleteFunc(f.Dependencies, func(name string) bool {
		duplicate := declared[name]
		declared[name] = true
		return duplicate
	})
	for tag, b := range f.Bottles {
		b.Tag = tag
		f.Bottles[tag] = b
	}
	return f, nil
}

// implicitDependencies are what unpacking the source archive needs. They are
// Homebrew's macOS rules for the download strategies homebrew/core uses; brew
// checks its fixed system PATH for zstd, which macOS does not ship.
func implicitDependencies(raw rawFormula) []model.DeclaredDependency {
	var names []string
	using, url := raw.URLs.Stable.Using, raw.URLs.Stable.URL
	if using == "homebrew_curl" {
		names = append(names, "curl")
	}
	if using == nil || using == "homebrew_curl" {
		if name, ok := map[string]string{".zst": "zstd", ".lha": "lha", ".lzh": "lha", ".lz": "lzip", ".rar": "libarchive", ".7z": "p7zip"}[path.Ext(url)]; ok {
			names = append(names, name)
		}
	}
	implicit := make([]model.DeclaredDependency, len(names))
	for i, name := range names {
		implicit[i] = model.DeclaredDependency{Name: name, Build: true, Test: true, Implicit: true}
	}
	return implicit
}

// declaredDependencies follows Homebrew's API loader: implicit dependencies,
// plain ones, then each tagged name once, with every tag it has.
func declaredDependencies(raw rawFormula) []model.DeclaredDependency {
	seen := map[string]bool{}
	declared := implicitDependencies(raw)
	for _, name := range raw.Dependencies {
		if !seen[name] {
			seen[name] = true
			declared = append(declared, model.DeclaredDependency{Name: name, NoLinkage: slices.Contains(raw.NoLinkageDependencies, name)})
		}
	}
	clear(seen)
	for _, names := range [][]string{raw.Build, raw.Test, raw.Recommended, raw.Optional} {
		for _, name := range names {
			if !seen[name] {
				seen[name] = true
				declared = append(declared, model.DeclaredDependency{
					Name: name, Build: slices.Contains(raw.Build, name), Test: slices.Contains(raw.Test, name),
					Recommended: slices.Contains(raw.Recommended, name), Optional: slices.Contains(raw.Optional, name),
					NoLinkage: slices.Contains(raw.NoLinkageDependencies, name),
				})
			}
		}
	}
	return declared
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
