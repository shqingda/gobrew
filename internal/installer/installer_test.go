package installer

import (
	"github.com/shqingda/gobrew/internal/model"
)

// onePackage is a plan that installs a single prepared tree.
func onePackage(prefix, tree string, f model.Formula) model.Prepared {
	return model.Prepared{Plan: model.Plan{Prefix: prefix}, Packages: []model.PreparedPackage{{Package: model.Package{Formula: f}, Tree: tree}}}
}
