package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shqingda/gobrew/internal/engine"
	"github.com/shqingda/gobrew/internal/selfupdate"
)

// staleCatalog is when doctor suggests `gobrew update`; auto-update keeps a
// catalog younger than a day unless it is disabled.
const staleCatalog = 7 * 24 * time.Hour

// environmentFindings are doctor's warnings about the platform, the catalog,
// the cache, the gobrew CLI and the shell startup file. None of them makes the
// installation unhealthy, and checking changes nothing.
func (a *application) environmentFindings(e *engine.Engine) []engine.Finding {
	var findings []engine.Finding
	warn := func(problem string) {
		findings = append(findings, engine.Finding{Problem: problem, Severity: "warning"})
	}
	if _, err := e.Host(); err != nil {
		warn(userMessage(err))
	}
	switch fetched, err := e.CatalogFetched(); {
	case errors.Is(err, os.ErrNotExist):
		warn("no formula catalog has been downloaded yet; run `gobrew update`")
	case err != nil:
		warn("the formula catalog cannot be read: " + userMessage(err) + "; run `gobrew update`")
	case time.Since(fetched) > staleCatalog:
		warn(fmt.Sprintf("the formula catalog was last refreshed %d days ago; run `gobrew update`", int(time.Since(fetched)/(24*time.Hour))))
	}
	if dir := existingParent(e.CacheDir()); dir != "" && syscall.Access(dir, 2 /* W_OK */) != nil {
		warn(dir + " is not writable, so downloads cannot be cached; fix its permissions or pass --cache-dir")
	}
	if _, err := selfupdate.Running(); err != nil {
		warn("this gobrew executable is not managed by `gobrew self install`, so `gobrew update` cannot update it")
	}
	if p, err := a.profile(); err == nil {
		if err := p.Check(); err != nil {
			warn(err.Error() + "; fix or remove the gobrew lines so gobrew can update them")
		}
	}
	return findings
}

// existingParent is path or its nearest existing ancestor.
func existingParent(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}
