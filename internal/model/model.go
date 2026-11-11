// Package model defines values shared by the planner and installation pipeline.
// It has no platform, database or presentation dependencies.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrUnsupported      = errors.New("unsupported")
	ErrNotFound         = errors.New("not found")
	ErrConflict         = errors.New("conflict")
	ErrBusy             = errors.New("another operation is in progress")
	ErrStalePlan        = errors.New("installation state changed; create a new plan")
	ErrNeedsRepair      = errors.New("installation needs repair")
	ErrRecoveryRequired = fmt.Errorf("%w: interrupted operation requires recovery", ErrNeedsRepair)

	// ErrPrivilegeRequired means the prefix parent needs administrator access.
	// Callers decide whether and how to obtain it; the engine never prompts.
	ErrPrivilegeRequired = errors.New("administrator access required")
)

type Platform struct {
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	Major int    `json:"major"`
}

// deprecationReasons are the named reasons of deprecate! and disable!.
var deprecationReasons = map[string]string{
	"does_not_build":      "does not build",
	"no_license":          "has no license",
	"repo_archived":       "has an archived upstream repository",
	"repo_removed":        "has a removed upstream repository",
	"unmaintained":        "is not maintained upstream",
	"unreachable":         "is no longer reliably reachable upstream",
	"unsupported":         "is not supported upstream",
	"deprecated_upstream": "is deprecated upstream",
	"versioned_formula":   "is a versioned formula",
	"checksum_mismatch": "was built with an initially released source file that had a different checksum than the current one. " +
		"Upstream's repository might have been compromised. We can re-package this once upstream has confirmed that they retagged their release",
}

// DeprecationMessage ports Homebrew's DeprecateDisable.message, or is empty for
// a formula that is neither deprecated nor disabled. Dates are ISO strings, so
// they compare as text.
func (f Formula) DeprecationMessage(today time.Time) string {
	if !f.Deprecated && !f.Disabled {
		return ""
	}
	kind, reason := "disabled", f.DisableReason
	if f.Deprecated {
		kind, reason = "deprecated", f.DeprecationReason
	}
	if text, ok := deprecationReasons[reason]; ok {
		reason = text
	}
	message := kind + "!"
	if reason != "" {
		message = kind + " because it " + reason + "!"
	}
	now := today.Format(time.DateOnly)
	date := f.DisableDate
	if deprecated, err := time.Parse(time.DateOnly, f.DeprecationDate); date == "" && err == nil {
		if estimate := deprecated.AddDate(1, 0, 0).Format(time.DateOnly); estimate >= now {
			date = estimate
		}
	}
	if date != "" && date < now {
		message += " It was disabled on " + date + "."
	} else if date != "" {
		message += " It will be disabled on " + date + "."
	}
	formula, cask := f.DeprecationFormula, f.DeprecationCask
	if f.Disabled {
		formula, cask = f.DisableFormula, f.DisableCask
	}
	if formula != "" {
		message += "\nReplacement:\n  gobrew install " + formula
	} else if cask != "" {
		message += "\nReplacement:\n  brew install --cask " + cask
	}
	return message
}

// DeclaredDependency is one depends_on line with its tags, in formula order.
// Only deps reads it: bottles install from Dependencies alone.
type DeclaredDependency struct {
	Name        string `json:"name"`
	Build       bool   `json:"build,omitempty"`
	Test        bool   `json:"test,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
	NoLinkage   bool   `json:"no_linkage,omitempty"`
	Implicit    bool   `json:"implicit,omitempty"`
}

type Formula struct {
	Name                  string               `json:"name"`
	Aliases               []string             `json:"aliases,omitempty"`
	Description           string               `json:"description"`
	Homepage              string               `json:"homepage,omitempty"`
	License               string               `json:"license,omitempty"`
	Version               string               `json:"version"`
	Revision              int                  `json:"revision"`
	VersionScheme         int                  `json:"version_scheme"`
	CompatibilityVersion  *int                 `json:"compatibility_version,omitempty"`
	BottleRebuild         int                  `json:"bottle_rebuild"`
	Bottles               map[string]Bottle    `json:"bottles"`
	Dependencies          []string             `json:"dependencies,omitempty"`
	NoLinkageDependencies []string             `json:"no_linkage_dependencies,omitempty"`
	DeclaredDependencies  []DeclaredDependency `json:"declared_dependencies,omitempty"`
	RuntimeDependencies   []RuntimeDependency  `json:"runtime_dependencies,omitempty"`
	KegOnly               bool                 `json:"keg_only"`
	KegOnlyReason         string               `json:"keg_only_reason,omitempty"`
	KegOnlyExplanation    string               `json:"keg_only_explanation,omitempty"`
	Disabled              bool                 `json:"disabled"`
	Deprecated            bool                 `json:"deprecated"`
	DeprecationDate       string               `json:"deprecation_date,omitempty"`
	DeprecationReason     string               `json:"deprecation_reason,omitempty"`
	DeprecationFormula    string               `json:"deprecation_replacement_formula,omitempty"`
	DeprecationCask       string               `json:"deprecation_replacement_cask,omitempty"`
	DisableDate           string               `json:"disable_date,omitempty"`
	DisableReason         string               `json:"disable_reason,omitempty"`
	DisableFormula        string               `json:"disable_replacement_formula,omitempty"`
	DisableCask           string               `json:"disable_replacement_cask,omitempty"`
	Caveats               string               `json:"caveats,omitempty"`
	PostInstallDefined    bool                 `json:"post_install_defined"`
	Steps                 []json.RawMessage    `json:"post_install_steps,omitempty"`
	UnsupportedReason     string               `json:"unsupported_reason,omitempty"`
	Conflicts             []string             `json:"conflicts,omitempty"`
	// ConflictReasons parallels Conflicts; an entry may be empty.
	ConflictReasons []string `json:"conflict_reasons,omitempty"`
	// SourcePath is the recipe's path in homebrew-core, for display only.
	SourcePath string `json:"source_path,omitempty"`
}

// KegOnlyVersioned marks an alternate version, which brew links without --force.
const KegOnlyVersioned = ":versioned_formula"

func (f Formula) PkgVersion() string {
	if f.Revision > 0 {
		return fmt.Sprintf("%s_%d", f.Version, f.Revision)
	}
	return f.Version
}

type Bottle struct {
	Tag    string `json:"tag"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Cellar string `json:"cellar"`
}

type RuntimeDependency struct {
	Name                 string `json:"full_name"`
	Version              string `json:"version"`
	Revision             int    `json:"revision"`
	PkgVersion           string `json:"pkg_version,omitempty"`
	BottleRebuild        int    `json:"bottle_rebuild,omitempty"`
	CompatibilityVersion *int   `json:"compatibility_version,omitempty"`
	DeclaredDirectly     bool   `json:"declared_directly,omitempty"`
}

type Record struct {
	Formula             Formula             `json:"formula"`
	Bottle              Bottle              `json:"bottle"`
	Keg                 string              `json:"keg"`
	Owner               string              `json:"owner"`
	Dependencies        []RuntimeDependency `json:"dependencies,omitempty"`
	Requested           bool                `json:"requested"`
	Pinned              bool                `json:"pinned"`
	Linked              bool                `json:"linked"`
	Status              string              `json:"status"`
	Links               map[string]string   `json:"links,omitempty"`
	InstalledAt         time.Time           `json:"installed_at"`
	PostInstallError    string              `json:"post_install_error,omitempty"`
	PostInstallComplete bool                `json:"post_install_complete"`
}

// LinkResult reports what link or unlink changes for one keg. Paths are
// absolute. AlreadyLinked and NeedsForce mean link left the keg alone; KegOnly
// means it needs brew's PATH advice even once linked.
type LinkResult struct {
	Name          string   `json:"name"`
	Keg           string   `json:"keg"`
	Paths         []string `json:"paths"`
	AlreadyLinked bool     `json:"already_linked,omitempty"`
	NeedsForce    bool     `json:"needs_force,omitempty"`
	KegOnly       bool     `json:"keg_only,omitempty"`
}

const (
	Ready            = "ready"
	NeedsPostInstall = "needs-postinstall"
)

type Snapshot struct {
	Generation uint64            `json:"generation"`
	Records    map[string]Record `json:"records"`
	// RecoveryRequired is derived from the journal in the same database read.
	// It is never persisted as part of the installed snapshot.
	RecoveryRequired bool `json:"-"`
}

type Request struct {
	Action string   `json:"action"`
	Names  []string `json:"names"`
	// Versions holds versions requested through version-install, keyed by
	// formula name. Plain install names are never split at "@", so python@3.13
	// always means that formula, as in Homebrew.
	Versions map[string]string `json:"versions,omitempty"`
	// Exact lists formulae whose requested version the resolver may install in
	// place of, or below, the installed version. Plan derives it from Versions.
	Exact []string `json:"exact,omitempty"`
	// NoInstallUpgrade keeps install from upgrading an installed but outdated
	// requested formula (Homebrew's HOMEBREW_NO_INSTALL_UPGRADE).
	NoInstallUpgrade bool `json:"no_install_upgrade,omitempty"`
	// AsDependency installs without marking the named formulae as requested.
	AsDependency bool `json:"as_dependency,omitempty"`
	// OnlyDependencies installs what the named formulae need but not themselves.
	OnlyDependencies bool `json:"only_dependencies,omitempty"`
	// SkipLink and SkipPostInstall apply to the named formulae, not dependencies.
	SkipLink        bool `json:"skip_link,omitempty"`
	SkipPostInstall bool `json:"skip_post_install,omitempty"`
	// Force is brew's --force for bottles: disabled formulae install with a
	// warning instead of an error. conflicts_with is still checked.
	Force bool `json:"force,omitempty"`
	// IncludeTest also installs the test dependencies the named formulae
	// declare, like brew install --include-test.
	IncludeTest bool `json:"include_test,omitempty"`
}

// MarksRequested reports whether the named formulae become requested roots.
func (r Request) MarksRequested() bool { return !r.AsDependency && !r.OnlyDependencies }

type Package struct {
	Formula   Formula `json:"formula"`
	Bottle    Bottle  `json:"bottle"`
	Requested bool    `json:"requested"`
	// Historic marks an explicitly requested older version whose bottle was
	// verified against GHCR and homebrew-core instead of signed metadata.
	Historic bool `json:"historic,omitempty"`
	// SkipLink and SkipPostInstall carry Request's options for a named formula.
	SkipLink        bool `json:"skip_link,omitempty"`
	SkipPostInstall bool `json:"skip_post_install,omitempty"`
}

type Plan struct {
	ID                string    `json:"id"`
	Request           Request   `json:"request"`
	CatalogGeneration string    `json:"catalog_generation"`
	StateGeneration   uint64    `json:"state_generation"`
	Platform          Platform  `json:"platform"`
	Prefix            string    `json:"prefix"`
	Packages          []Package `json:"packages"`
	Unchanged         []string  `json:"unchanged,omitempty"`
	RuntimeChecks     []string  `json:"runtime_checks,omitempty"`
	Promote           []string  `json:"promote,omitempty"`
	// Upgrading maps requested, installed but outdated formulae that install
	// will upgrade to their installed version, for Homebrew's notice.
	Upgrading map[string]string `json:"upgrading,omitempty"`
	// Held lists requested, outdated formulae that install left alone because
	// they are pinned or NoInstallUpgrade is set, and pinned outdated formulae
	// that upgrade skipped. Homebrew reports named ones as errors.
	Held []string `json:"held,omitempty"`
}

type PreparedPackage struct {
	Package             Package             `json:"package"`
	Tree                string              `json:"tree"`
	RuntimeDependencies []RuntimeDependency `json:"runtime_dependencies,omitempty"`
	LinkedDependencies  []string            `json:"linked_dependencies,omitempty"`
	LinkageChecked      bool                `json:"linkage_checked"`
	CacheHit            bool                `json:"cache_hit"`
}

type Prepared struct {
	Plan     Plan              `json:"plan"`
	Packages []PreparedPackage `json:"packages"`
}

type Result struct {
	Succeeded   []string `json:"succeeded,omitempty"`
	Unchanged   []string `json:"unchanged,omitempty"`
	NeedsRepair []string `json:"needs_repair,omitempty"`
	NotExecuted []string `json:"not_executed,omitempty"`
	Timings     *Timings `json:"timings_ms,omitempty"`

	// Historic lists explicit older versions installed outside the signed
	// catalog by this operation.
	Historic []string `json:"historic,omitempty"`
	// Held copies Plan.Held so clients can report them after the operation.
	Held []string `json:"held,omitempty"`
}

// Removed describes one path cleanup removed, or would remove in a dry run.
// Files is zero when only the logical size is known.
type Removed struct {
	Path  string `json:"path"`
	Files int    `json:"files,omitempty"`
	Bytes int64  `json:"bytes"`
	// Kind marks what pruning the prefix removed; kegs and cache entries have none.
	Kind string `json:"kind,omitempty"`
}

const (
	BrokenLink     = "broken-link"
	EmptyDirectory = "empty-directory"
)

// Timings measures the application's three stages, excluding CLI rendering.
// Wall-clock process timing remains the end-to-end performance measure.
type Timings struct {
	Plan    float64 `json:"plan"`
	Prepare float64 `json:"prepare"`
	Apply   float64 `json:"apply"`
}

type Event struct {
	Phase     string `json:"phase"`
	Package   string `json:"package,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Emit never lets a slow renderer block an installation. The caller owns and
// closes the channel after the operation has returned; final results are returned.
func Emit(events chan<- Event, event Event) {
	select {
	case events <- event:
	default:
	}
}

// OwnerMarker names the file that records gobrew's ownership of a keg.
const OwnerMarker = ".gobrew-owner"

// UserAgent identifies gobrew's HTTP requests.
const UserAgent = "gobrew"

// ValidPathComponent reports whether s is safe as exactly one path element.
func ValidPathComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

// Analytics is one category of formulae.brew.sh install counts, such as
// install-on-request, with its periods in the order the API lists them.
type Analytics struct {
	Category string            `json:"category"`
	Periods  []AnalyticsPeriod `json:"periods"`
}

// AnalyticsPeriod is the count over a number of days: the total, and each
// formula variant ("jq --HEAD") in the order the API lists them.
type AnalyticsPeriod struct {
	Days    int              `json:"days"`
	Count   int64            `json:"count"`
	Results []AnalyticsCount `json:"results"`
}

// AnalyticsCount is one row of an analytics table.
type AnalyticsCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// PullRequest is one result of gobrew search --pull-request.
type PullRequest struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	State string `json:"state"`
}

// MacOSVersions maps Homebrew's macOS bottle tag names to major versions.
var MacOSVersions = map[string]int{"big_sur": 11, "monterey": 12, "ventura": 13, "sonoma": 14, "sequoia": 15, "tahoe": 26, "golden_gate": 27}

// MacOSName is the tag name for a macOS major version. A version newer than
// this build knows is treated as the newest known one, the same choice bottle
// selection makes; it returns "" only for versions older than every name.
func MacOSName(major int) string {
	name, best := "", 0
	for n, m := range MacOSVersions {
		if m <= major && m > best {
			name, best = n, m
		}
	}
	return name
}
