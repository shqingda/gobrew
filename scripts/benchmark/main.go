// Command benchmark measures isolated real installations with Go's monotonic clock.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/shqingda/gobrew/internal/model"
)

type measurement struct {
	Name         string    `json:"name"`
	Milliseconds []float64 `json:"milliseconds"`
	Median       float64   `json:"median_ms"`
	P95          float64   `json:"p95_ms"`
}

type report struct {
	SchemaVersion int               `json:"schema_version"`
	Timestamp     string            `json:"timestamp"`
	Platform      string            `json:"platform"`
	OSVersion     string            `json:"os_version"`
	Packages      []string          `json:"packages"`
	GobrewPrefix  string            `json:"gobrew_prefix"`
	BrewPrefix    string            `json:"brew_prefix,omitempty"`
	GobrewVersion string            `json:"gobrew_version"`
	BrewVersion   string            `json:"brew_version,omitempty"`
	BrewCommit    string            `json:"brew_commit,omitempty"`
	GobrewSHA256  string            `json:"gobrew_sha256"`
	SourceCommit  string            `json:"source_commit,omitempty"`
	RunnerImage   string            `json:"runner_image_version,omitempty"`
	GoVersion     string            `json:"go_version"`
	Measurements  []measurement     `json:"measurements"`
	Samples       []sample          `json:"samples"`
	Installed     json.RawMessage   `json:"installed"`
	BrewInstalled json.RawMessage   `json:"brew_installed,omitempty"`
	Versions      map[string]string `json:"matched_package_versions"`
	Notes         []string          `json:"notes"`
	Gate          *performanceGate  `json:"performance_gate,omitempty"`
	Error         string            `json:"error,omitempty"`
}

func (r *report) add(name string, duration time.Duration) {
	for i := range r.Measurements {
		if r.Measurements[i].Name == name {
			r.Measurements[i].Milliseconds = append(r.Measurements[i].Milliseconds, float64(duration)/float64(time.Millisecond))
			return
		}
	}
	r.Measurements = append(r.Measurements, measurement{Name: name, Milliseconds: []float64{float64(duration) / float64(time.Millisecond)}})
}

type runner struct {
	context context.Context
	binary  string
	prefix  []string
	env     []string
}

func (r runner) run(args ...string) (time.Duration, []byte, error) {
	command := exec.CommandContext(r.context, r.binary, append(slices.Clone(r.prefix), args...)...)
	command.Env = r.env
	command.Stderr = os.Stderr
	var output bytes.Buffer
	command.Stdout = &output
	start := time.Now()
	err := command.Run()
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, output.Bytes(), fmt.Errorf("%s %s: %w: %s", filepath.Base(r.binary), strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
	return elapsed, output.Bytes(), nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	bin := flag.String("gobrew", "./bin/gobrew", "built gobrew executable")
	packages := flag.String("packages", "hello,jq,ca-certificates", "comma-separated official formula names")
	rounds := flag.Int("rounds", 5, "cold, warm and already-installed sample count")
	brewBin := flag.String("brew", "", "optional disposable, initially empty Homebrew executable")
	brewPrefix := flag.String("brew-prefix", "", "optional expected prefix for the isolated Homebrew executable")
	requireFaster := flag.Bool("require-faster", false, "require at least five matched samples and faster cold/warm medians than --brew")
	flag.Parse()
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("real bottle benchmarks require Apple Silicon macOS")
	}
	if *rounds < 1 || *rounds > 100 {
		return errors.New("rounds must be between 1 and 100")
	}
	if *brewPrefix != "" && *brewBin == "" {
		return errors.New("brew-prefix requires --brew")
	}
	if *requireFaster && (*brewBin == "" || *rounds < 5) {
		return errors.New("require-faster requires --brew and at least five rounds")
	}
	names := strings.Split(*packages, ",")
	validName := regexp.MustCompile(`^[a-z0-9][a-z0-9+_.@-]*$`)
	for _, name := range names {
		if !validName.MatchString(name) {
			return fmt.Errorf("invalid formula name %q", name)
		}
	}
	binary, err := filepath.Abs(*bin)
	if err != nil {
		return err
	}
	if _, err = os.Stat(binary); err != nil {
		return fmt.Errorf("build gobrew first: %w", err)
	}
	prefix, err := shortPrefix()
	if err != nil {
		return err
	}
	defer os.RemoveAll(prefix)
	work, err := os.MkdirTemp("", "gobrew-benchmark-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := report{SchemaVersion: 1, Timestamp: time.Now().UTC().Format(time.RFC3339), Platform: runtime.GOOS + "/" + runtime.GOARCH, Packages: names, GobrewPrefix: prefix, SourceCommit: os.Getenv("GITHUB_SHA"), RunnerImage: os.Getenv("ImageVersion"), GoVersion: runtime.Version(), Notes: []string{
		"Wall-clock process durations in milliseconds. Each round measures cold, warm and no-op independently; no speed ratios are fabricated.",
		"Metadata is refreshed outside install timing and measured separately. Cold installs start with empty artifact caches.",
		"Warm installs reset installed state and mutable configuration, retaining that round's artifact cache; full post-install commands still run.",
		"Gobrew uses one generated prefix throughout. Optional Homebrew must have no installed formulae; its initial etc/var are restored between samples and after completion.",
		"The two tools use separate prefixes recorded in this report; prefix-specific relocation work can differ.",
		"Homebrew installs use --force-bottle, without skipping dependencies, linking, verification or post-install.",
		"Homebrew uses system tools on PATH and isolated cache/log directories; automatic catalog refresh, analytics and automatic cleanup are disabled outside the explicit sample lifecycle.",
		"The baseline ignores user brew.env via an isolated XDG config directory and refuses system/prefix brew.env files that could override isolation.",
		"After every cold and warm installation, executable and certificate fixture checks run outside the timed interval.",
		"Stage timings come from gobrew's install result; the release gate uses full process wall time. Homebrew does not expose matching internal stages.",
		"Disk samples are collected after each scenario, outside wall timing. Logical bytes sum regular file lengths without following symlinks; they do not measure APFS shared physical blocks. Homebrew prefix.other includes its repository and runtime.",
		"This is a small benchmark, not a performance guarantee; network, CPU and OS cache effects remain.",
	}}
	if *requireFaster {
		result.Gate = &performanceGate{}
	}
	// Preserve partial evidence and failed gate results, not just successful runs.
	defer func() {
		for i := range result.Measurements {
			m := &result.Measurements[i]
			m.Median, m.P95 = statistics(m.Milliseconds)
		}
		if runErr != nil {
			result.Error = runErr.Error()
			if result.Gate != nil {
				result.Gate.Passed = false
			}
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		runErr = errors.Join(runErr, encoder.Encode(result))
	}()
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(binaryBytes)
	result.GobrewSHA256 = hex.EncodeToString(digest[:])
	_, version, err := (runner{context: ctx, binary: binary, env: os.Environ()}).run("--version")
	if err != nil {
		return err
	}
	result.GobrewVersion = strings.TrimSpace(string(version))
	_, osVersion, err := (runner{context: ctx, binary: "/usr/bin/sw_vers", env: os.Environ()}).run("-productVersion")
	if err != nil {
		return err
	}
	result.OSVersion = strings.TrimSpace(string(osVersion))
	var baseline *brewBaseline
	if *brewBin != "" {
		baseline, err = openBaseline(ctx, *brewBin, *brewPrefix, filepath.Join(work, "brew-original"))
		if err != nil {
			return err
		}
		result.BrewPrefix = baseline.prefix
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := baseline.reset(cleanupCtx); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("baseline cleanup: %w", err))
			}
		}()
		_, v, e := baseline.runner.run("--version")
		if e != nil {
			return e
		}
		result.BrewVersion = strings.TrimSpace(string(v))
		_, repository, e := baseline.runner.run("--repository")
		if e != nil {
			return e
		}
		_, commit, e := (runner{context: ctx, binary: "/usr/bin/git", env: os.Environ()}).run("-C", strings.TrimSpace(string(repository)), "rev-parse", "HEAD")
		if e != nil {
			return e
		}
		result.BrewCommit = strings.TrimSpace(string(commit))
	}
	for round := range *rounds {
		fmt.Fprintf(os.Stderr, "Benchmark round %d/%d\n", round+1, *rounds)
		cache := filepath.Join(work, fmt.Sprintf("gobrew-%d", round))
		// Mirror the Homebrew side's HOMEBREW_NO_AUTO_UPDATE and NO_INSTALL_CLEANUP.
		gbEnv := append(os.Environ(), "GOBREW_NO_AUTO_UPDATE=1", "GOBREW_NO_INSTALL_CLEANUP=1")
		gb := runner{context: ctx, binary: binary, prefix: []string{"--prefix", prefix, "--cache-dir", cache, "--json"}, env: gbEnv}
		if err = measureGobrew(gb, prefix, cache, round+1, names, &result); err != nil {
			return err
		}
		if baseline != nil {
			baseline.cache = filepath.Join(work, fmt.Sprintf("brew-%d", round))
			baseline.runner.env = brewEnvironment(baseline.cache)
			if err = baseline.measure(round+1, names, &result); err != nil {
				return err
			}
		}
	}
	if baseline != nil {
		if err = baseline.reset(ctx); err != nil {
			return err
		}
	}
	if *requireFaster {
		gate, gateErr := checkPerformance(result.Measurements)
		result.Gate = &gate
		return gateErr
	}
	return nil
}

func measureGobrew(gb runner, prefix, cache string, round int, names []string, result *report) error {
	if err := resetOwnedPrefix(prefix); err != nil {
		return err
	}
	if _, _, err := gb.run("init"); err != nil {
		return err
	}
	duration, _, err := gb.run("update")
	if err != nil {
		return err
	}
	result.add("gobrew.catalog", duration)
	install := append([]string{"install"}, names...)
	duration, output, err := gb.run(install...)
	if err != nil {
		return err
	}
	result.add("gobrew.cold", duration)
	if err = result.sample(gb.context, "gobrew", "cold", round, duration, prefix, cache, output); err != nil {
		return err
	}
	if err = checkWorkload(gb.context, prefix, names); err != nil {
		return err
	}
	if err = resetOwnedPrefix(prefix); err != nil {
		return err
	}
	if _, _, err = gb.run("init"); err != nil {
		return err
	}
	duration, output, err = gb.run(install...)
	if err != nil {
		return err
	}
	result.add("gobrew.warm", duration)
	if err = result.sample(gb.context, "gobrew", "warm", round, duration, prefix, cache, output); err != nil {
		return err
	}
	if err = checkWorkload(gb.context, prefix, names); err != nil {
		return err
	}
	duration, output, err = gb.run(install...)
	if err != nil {
		return err
	}
	result.add("gobrew.noop", duration)
	if err = result.sample(gb.context, "gobrew", "noop", round, duration, prefix, cache, output); err != nil {
		return err
	}
	_, installed, err := gb.run("list")
	if err != nil {
		return err
	}
	var envelope struct {
		Data []model.Record `json:"data"`
	}
	if err = json.Unmarshal(installed, &envelope); err != nil {
		return fmt.Errorf("gobrew list: %w", err)
	}
	versions := map[string]string{}
	for _, record := range envelope.Data {
		if record.Status != model.Ready {
			return fmt.Errorf("gobrew package %s is not ready", record.Formula.Name)
		}
		versions[record.Formula.Name] = record.Formula.PkgVersion()
	}
	if len(versions) == 0 {
		return errors.New("gobrew list returned no installed formulae")
	}
	if result.Versions != nil && !maps.Equal(result.Versions, versions) {
		return errors.New("gobrew package versions changed during benchmark")
	}
	result.Versions = versions
	result.Installed = installed
	return nil
}

func shortPrefix() (string, error) {
	for range 20 {
		var random [2]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		path := "/tmp/g" + hex.EncodeToString(random[:])
		if err := os.Mkdir(path, 0700); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not allocate a short temporary prefix")
}

func resetOwnedPrefix(prefix string) error {
	if err := os.RemoveAll(prefix); err != nil {
		return err
	}
	return os.Mkdir(prefix, 0700)
}

type brewBaseline struct {
	runner    runner
	prefix    string
	backup    string
	cache     string
	installed []string
}

func openBaseline(ctx context.Context, binary, expected, backup string) (*brewBaseline, error) {
	if err := rejectBaselineConfig("/etc/homebrew/brew.env"); err != nil {
		return nil, err
	}
	// A disposable prefix may live under /tmp. Homebrew forbids putting its
	// prefix inside HOMEBREW_TEMP, so give it a separate owned work directory.
	if err := os.MkdirAll(filepath.Join(filepath.Dir(backup), "brew-temp"), 0700); err != nil {
		return nil, err
	}
	b := &brewBaseline{runner: runner{context: ctx, binary: binary, env: brewEnvironment(backup + "-cache")}, backup: backup}
	_, reported, err := b.runner.run("--prefix")
	if err != nil {
		return nil, err
	}
	prefix, err := filepath.EvalSymlinks(strings.TrimSpace(string(reported)))
	if err != nil {
		return nil, err
	}
	prefix, err = filepath.Abs(prefix)
	if err != nil {
		return nil, err
	}
	if prefix == "/opt/homebrew" || prefix == "/usr/local" || prefix == "/" {
		return nil, errors.New("brew baseline must use a separate disposable prefix")
	}
	if err = rejectBaselineConfig(filepath.Join(prefix, "etc", "homebrew", "brew.env")); err != nil {
		return nil, err
	}
	if expected != "" {
		want, err := filepath.EvalSymlinks(expected)
		if err != nil {
			return nil, err
		}
		want, err = filepath.Abs(want)
		if err != nil || want != prefix {
			return nil, errors.New("brew executable does not use the expected isolated prefix")
		}
	}
	_, existing, err := b.runner.run("list", "--formula")
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(existing))) != 0 {
		return nil, errors.New("brew baseline prefix already contains formulae; refusing to mutate it")
	}
	b.prefix = prefix
	for _, name := range []string{"etc", "var"} {
		if err = copyPath(filepath.Join(prefix, name), filepath.Join(backup, name)); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func rejectBaselineConfig(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("isolated Homebrew benchmark refuses %s because it can override cache and initialization settings", path)
}

func (b *brewBaseline) reset(ctx context.Context) error {
	r := b.runner
	r.context = ctx
	if len(b.installed) > 0 {
		if _, _, err := r.run(append([]string{"uninstall", "--force", "--formula", "--ignore-dependencies"}, b.installed...)...); err != nil {
			return err
		}
		b.installed = nil
	}
	for _, name := range []string{"etc", "var"} {
		target := filepath.Join(b.prefix, name)
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := copyPath(filepath.Join(b.backup, name), target); err != nil {
			return err
		}
	}
	return nil
}

func (b *brewBaseline) install(names []string) (time.Duration, error) {
	duration, _, installErr := b.runner.run(append([]string{"install", "--formula", "--force-bottle"}, names...)...)
	// Discover partial successes after failure for cleanup of this empty prefix.
	r := b.runner
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r.context = ctx
	_, installed, listErr := r.run("list", "--formula")
	if listErr != nil {
		return duration, listErr
	}
	b.installed = strings.Fields(string(installed))
	return duration, installErr
}

func (b *brewBaseline) measure(round int, names []string, result *report) error {
	if err := b.reset(b.runner.context); err != nil {
		return err
	}
	duration, _, err := b.runner.run(append([]string{"info", "--json=v2", "--formula"}, names...)...)
	if err != nil {
		return err
	}
	result.add("brew.catalog", duration)
	duration, err = b.install(names)
	if err != nil {
		return err
	}
	result.add("brew.cold", duration)
	if err = result.sample(b.runner.context, "brew", "cold", round, duration, b.prefix, b.cache, nil); err != nil {
		return err
	}
	if err = checkWorkload(b.runner.context, b.prefix, names); err != nil {
		return err
	}
	if err = b.reset(b.runner.context); err != nil {
		return err
	}
	duration, err = b.install(names)
	if err != nil {
		return err
	}
	result.add("brew.warm", duration)
	if err = result.sample(b.runner.context, "brew", "warm", round, duration, b.prefix, b.cache, nil); err != nil {
		return err
	}
	if err = checkWorkload(b.runner.context, b.prefix, names); err != nil {
		return err
	}
	duration, _, err = b.runner.run(append([]string{"install", "--formula", "--force-bottle"}, names...)...)
	if err != nil {
		return err
	}
	result.add("brew.noop", duration)
	if err = result.sample(b.runner.context, "brew", "noop", round, duration, b.prefix, b.cache, nil); err != nil {
		return err
	}
	_, info, err := b.runner.run("info", "--json=v2", "--installed")
	if err != nil {
		return err
	}
	var metadata struct {
		Formulae []struct {
			Name      string `json:"name"`
			Installed []struct {
				Version string `json:"version"`
				Poured  bool   `json:"poured_from_bottle"`
			} `json:"installed"`
		} `json:"formulae"`
	}
	if err = json.Unmarshal(info, &metadata); err != nil {
		return err
	}
	versions := map[string]string{}
	for _, f := range metadata.Formulae {
		if len(f.Installed) != 1 || !f.Installed[0].Poured {
			return fmt.Errorf("brew %s did not install exactly one bottle", f.Name)
		}
		versions[f.Name] = f.Installed[0].Version
	}
	if !maps.Equal(result.Versions, versions) {
		return fmt.Errorf("baseline package sets/versions differ: gobrew=%v brew=%v", result.Versions, versions)
	}
	result.BrewInstalled = info
	return nil
}

func checkWorkload(ctx context.Context, prefix string, names []string) error {
	for _, name := range names {
		r := runner{context: ctx, binary: filepath.Join(prefix, "bin", name), env: os.Environ()}
		switch name {
		case "hello":
			if _, _, err := r.run("--version"); err != nil {
				return fmt.Errorf("installed hello check: %w", err)
			}
		case "jq":
			_, output, err := r.run("-n", "21 * 2")
			if err != nil || strings.TrimSpace(string(output)) != "42" {
				return fmt.Errorf("installed jq check returned %q: %v", output, err)
			}
		case "ca-certificates":
			info, err := os.Stat(filepath.Join(prefix, "etc", "ca-certificates", "cert.pem"))
			if err != nil {
				return fmt.Errorf("post-install certificate check: %w", err)
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				return errors.New("post-install certificate bundle is missing or empty")
			}
		}
	}
	return nil
}

func brewEnvironment(cache string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HOMEBREW_") && !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "XDG_CONFIG_HOME=") {
			env = append(env, entry)
		}
	}
	return append(env, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "XDG_CONFIG_HOME="+filepath.Join(filepath.Dir(cache), "brew-config"), "HOMEBREW_TEMP="+filepath.Join(filepath.Dir(cache), "brew-temp"), "HOMEBREW_CACHE="+cache, "HOMEBREW_LOGS="+filepath.Join(filepath.Dir(cache), "brew-logs"), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ANALYTICS=1", "HOMEBREW_NO_INSTALL_CLEANUP=1")
}

// Snapshot only the original mutable directories, outside measured intervals.
// Symlinks are copied as symlinks; the Homebrew repository is never removed.
func copyPath(source, target string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(link, target)
	case info.IsDir():
		if err = os.MkdirAll(target, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err = copyPath(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		return errors.Join(copyErr, closeErr)
	default:
		return fmt.Errorf("baseline contains unsupported filesystem object %s", source)
	}
}
