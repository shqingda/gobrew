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
	ErrUnsupported = errors.New("unsupported")
	ErrNotFound    = errors.New("not found")
	ErrConflict    = errors.New("conflict")
	ErrBusy        = errors.New("another operation is in progress")
	ErrStalePlan   = errors.New("installation state changed; create a new plan")
	ErrNeedsRepair = errors.New("installation needs repair")
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
}

type Request struct {
	Action string   `json:"action"`
	Names  []string `json:"names"`
}

type Package struct {
	Formula   Formula `json:"formula"`
	Bottle    Bottle  `json:"bottle"`
	Requested bool    `json:"requested"`
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
	Promote           []string  `json:"promote,omitempty"`
}

type PreparedPackage struct {
	Package             Package             `json:"package"`
	Tree                string              `json:"tree"`
	RuntimeDependencies []RuntimeDependency `json:"runtime_dependencies,omitempty"`
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
