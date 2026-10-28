// Package model defines values shared by the planner and installation pipeline.
// It has no platform, database or presentation dependencies.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
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

type Formula struct {
	Name                  string              `json:"name"`
	Aliases               []string            `json:"aliases,omitempty"`
	Description           string              `json:"description"`
	Homepage              string              `json:"homepage,omitempty"`
	License               string              `json:"license,omitempty"`
	Version               string              `json:"version"`
	Revision              int                 `json:"revision"`
	VersionScheme         int                 `json:"version_scheme"`
	CompatibilityVersion  *int                `json:"compatibility_version,omitempty"`
	BottleRebuild         int                 `json:"bottle_rebuild"`
	Bottles               map[string]Bottle   `json:"bottles"`
	Dependencies          []string            `json:"dependencies,omitempty"`
	NoLinkageDependencies []string            `json:"no_linkage_dependencies,omitempty"`
	RuntimeDependencies   []RuntimeDependency `json:"runtime_dependencies,omitempty"`
	KegOnly               bool                `json:"keg_only"`
	Disabled              bool                `json:"disabled"`
	Deprecated            bool                `json:"deprecated"`
	Caveats               string              `json:"caveats,omitempty"`
	PostInstallDefined    bool                `json:"post_install_defined"`
	Steps                 []json.RawMessage   `json:"post_install_steps,omitempty"`
	UnsupportedReason     string              `json:"unsupported_reason,omitempty"`
	Conflicts             []string            `json:"conflicts,omitempty"`
}

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
}

type Package struct {
	Formula   Formula `json:"formula"`
	Bottle    Bottle  `json:"bottle"`
	Requested bool    `json:"requested"`
	// Pin keeps an explicitly requested older version out of upgrades.
	Pin bool `json:"pin,omitempty"`
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
	// they are pinned or NoInstallUpgrade is set. Homebrew reports these as errors.
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

	// Pinned lists explicit older versions installed outside the signed
	// catalog and pinned by this operation.
	Pinned []string `json:"pinned,omitempty"`
	// Held copies Plan.Held so clients can report them after the operation.
	Held []string `json:"held,omitempty"`
}

// Removed describes one path cleanup removed, or would remove in a dry run.
// Files is zero when only the logical size is known.
type Removed struct {
	Path  string `json:"path"`
	Files int    `json:"files,omitempty"`
	Bytes int64  `json:"bytes"`
}

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
