// Package catalog verifies Homebrew's public catalog and publishes immutable
// platform-specific indexes. Failed refreshes never replace the active snapshot.
package catalog

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shqingda/gobrew/internal/model"
	bolt "go.etcd.io/bbolt"
)

const defaultURL = "https://formulae.brew.sh/api/formula.jws.json"
const schemaVersion = "1"
const maxCatalogBytes = 128 << 20

type Snapshot struct {
	Generation string
	Formulae   map[string]model.Formula
}

type Client struct {
	Dir      string
	HTTP     *http.Client
	Platform model.Platform
	// URL and Key permit explicit trusted mirror and test configurations. The
	// default always uses HTTPS and the compiled-in official Homebrew key.
	URL string
	Key *rsa.PublicKey
}

func (s Snapshot) Lookup(name string) (model.Formula, error) {
	name = strings.TrimPrefix(name, "homebrew/core/")
	if f, ok := s.Formulae[name]; ok {
		return f, nil
	}
	var found *model.Formula
	for _, f := range s.Formulae {
		for _, alias := range f.Aliases {
			if alias == name {
				if found != nil {
					return model.Formula{}, fmt.Errorf("%w: ambiguous alias %s", model.ErrConflict, name)
				}
				copy := f
				found = &copy
			}
		}
	}
	if found != nil {
		return *found, nil
	}
	return model.Formula{}, fmt.Errorf("%w: formula %s", model.ErrNotFound, name)
}

func (s Snapshot) Search(query string) []model.Formula {
	query = strings.ToLower(query)
	result := []model.Formula{}
	for _, f := range s.Formulae {
		if strings.Contains(strings.ToLower(f.Name+" "+f.Description+" "+strings.Join(f.Aliases, " ")), query) {
			result = append(result, f)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (c Client) Update(ctx context.Context) (Snapshot, error) {
	endpoint := c.URL
	if endpoint == "" {
		endpoint = defaultURL
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(c.Key != nil && u.Scheme == "http")) {
		return Snapshot{}, errors.New("catalog URL must use HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("User-Agent", "gobrew")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	res, err := client.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("download catalog: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("download catalog: HTTP %s", res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxCatalogBytes+1))
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) > maxCatalogBytes {
		return Snapshot{}, errors.New("catalog exceeds size limit")
	}
	key := c.Key
	if key == nil {
		key, err = officialKey()
		if err != nil {
			return Snapshot{}, err
		}
	}
	payload, err := verify(data, key)
	if err != nil {
		return Snapshot{}, err
	}
	var raw []json.RawMessage
	if err = json.Unmarshal(payload, &raw); err != nil {
		return Snapshot{}, fmt.Errorf("catalog payload: %w", err)
	}
	if len(raw) == 0 {
		return Snapshot{}, errors.New("refusing an empty catalog")
	}
	platformJSON, _ := json.Marshal(c.Platform)
	hash := sha256.New()
	_, _ = hash.Write([]byte(schemaVersion))
	_, _ = hash.Write(platformJSON)
	_, _ = hash.Write(payload)
	snapshot := Snapshot{Generation: hex.EncodeToString(hash.Sum(nil)), Formulae: make(map[string]model.Formula, len(raw))}
	for _, entry := range raw {
		if err = ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		f, e := normalize(entry, c.Platform)
		if e != nil {
			return Snapshot{}, e
		}
		if _, duplicate := snapshot.Formulae[f.Name]; duplicate {
			return Snapshot{}, fmt.Errorf("duplicate formula %q", f.Name)
		}
		snapshot.Formulae[f.Name] = f
	}
	if err = c.publish(ctx, snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (c Client) publish(ctx context.Context, snapshot Snapshot) error {
	if c.Dir == "" {
		return errors.New("catalog directory is empty")
	}
	dir := filepath.Join(c.Dir, "snapshots")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".index-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err = f.Close(); err != nil {
		return err
	}
	defer os.Remove(tmp)
	db, err := bolt.Open(tmp, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		meta, e := tx.CreateBucket([]byte("meta"))
		if e != nil {
			return e
		}
		platformJSON, _ := json.Marshal(c.Platform)
		for key, value := range map[string][]byte{"generation": []byte(snapshot.Generation), "platform": platformJSON, "schema": []byte(schemaVersion)} {
			if e = meta.Put([]byte(key), value); e != nil {
				return e
			}
		}
		formulas, e := tx.CreateBucket([]byte("formulae"))
		if e != nil {
			return e
		}
		for name, formula := range snapshot.Formulae {
			if e = ctx.Err(); e != nil {
				return e
			}
			encoded, e := json.Marshal(formula)
			if e != nil {
				return e
			}
			if e = formulas.Put([]byte(name), encoded); e != nil {
				return e
			}
		}
		return nil
	})
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Link publishes a complete immutable inode without replacing an existing
	// generation that another process might currently be reading.
	if err = os.Chmod(tmp, 0400); err != nil {
		return err
	}
	if err = os.Link(tmp, filepath.Join(dir, snapshot.Generation+".db")); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err = syncDir(dir); err != nil {
		return err
	}
	return atomicWrite(c.Dir, "CURRENT", []byte(snapshot.Generation+"\n"))
}

func (c Client) Load() (Snapshot, error) {
	current, err := os.ReadFile(filepath.Join(c.Dir, "CURRENT"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("load catalog (run gobrew update): %w", err)
	}
	generation := strings.TrimSpace(string(current))
	decoded, err := hex.DecodeString(generation)
	if err != nil || len(decoded) != sha256.Size {
		return Snapshot{}, errors.New("invalid catalog generation")
	}
	db, err := bolt.Open(filepath.Join(c.Dir, "snapshots", generation+".db"), 0400, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return Snapshot{}, err
	}
	defer db.Close()
	snapshot := Snapshot{Generation: generation, Formulae: map[string]model.Formula{}}
	err = db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("meta"))
		formulas := tx.Bucket([]byte("formulae"))
		if meta == nil || formulas == nil {
			return errors.New("invalid catalog index")
		}
		platformJSON, _ := json.Marshal(c.Platform)
		if string(meta.Get([]byte("generation"))) != generation || string(meta.Get([]byte("schema"))) != schemaVersion || string(meta.Get([]byte("platform"))) != string(platformJSON) {
			return errors.New("catalog platform or schema changed; run gobrew update")
		}
		return formulas.ForEach(func(k, v []byte) error {
			var f model.Formula
			if err := json.Unmarshal(v, &f); err != nil {
				return err
			}
			if string(k) != f.Name {
				return errors.New("catalog formula identity mismatch")
			}
			snapshot.Formulae[f.Name] = f
			return nil
		})
	})
	return snapshot, err
}

func atomicWrite(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".current-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
