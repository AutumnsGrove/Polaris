package mapsnap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// TileCache is the shared on-disk tile cache: one file per tile, keyed by tile
// source + z/x/y, so a place looked up once is free next time and switching the
// tile URL template can never serve another provider's tiles.
//
// Recency lives in the file's mtime, not atime: noatime/relatime mounts make
// atime unreliable, and every Get bumps mtime, so tiles you keep coming back to
// never age out while one-off places do (see Sweep). It is only ever touched by
// the Polaris process, which is why it lives outside the per-thread workspaces
// the sandbox can see.
type TileCache struct {
	Dir string
	// now is swappable for tests; nil means time.Now.
	now func() time.Time
}

// SourceID names a tile source for the cache directory. It is derived from the
// URL *template* (placeholders unexpanded), never from anything containing an API
// key, so the key can't end up in a path on disk or in a log line.
func SourceID(template string) string {
	sum := sha256.Sum256([]byte(template))
	return hex.EncodeToString(sum[:])[:12]
}

func (c *TileCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *TileCache) path(source string, z, x, y int) string {
	return filepath.Join(c.Dir, source, fmt.Sprint(z), fmt.Sprint(x), fmt.Sprintf("%d.tile", y))
}

// Get returns a cached tile's bytes and refreshes its recency.
func (c *TileCache) Get(source string, z, x, y int) ([]byte, bool) {
	p := c.path(source, z, x, y)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	now := c.clock()
	// Best effort: a failed touch only means this hit doesn't extend the tile's life.
	_ = os.Chtimes(p, now, now)
	return data, true
}

// Put stores a tile. The write goes through a temp file + rename so a concurrent
// Get (or a crash mid-write) never sees a half-written tile.
func (c *TileCache) Put(source string, z, x, y int, data []byte) error {
	p := c.path(source, z, x, y)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Sweep deletes tiles idle longer than maxIdle, then — if the cache is still over
// maxBytes — the least recently used tiles until it fits. Returns how many files
// were removed. maxIdle <= 0 / maxBytes <= 0 disable that rule.
func (c *TileCache) Sweep(maxIdle time.Duration, maxBytes int64) (int, error) {
	type entry struct {
		path string
		mod  time.Time
		size int64
	}
	var files []entry
	var total int64
	err := filepath.WalkDir(c.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // vanished mid-walk
		}
		files = append(files, entry{p, info.ModTime(), info.Size()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}

	removed := 0
	remove := func(e entry) {
		if os.Remove(e.path) == nil {
			removed++
			total -= e.size
			// Drop now-empty z/x directories; Remove fails harmlessly if not empty.
			for dir := filepath.Dir(e.path); dir != c.Dir && len(dir) > len(c.Dir); dir = filepath.Dir(dir) {
				if os.Remove(dir) != nil {
					break
				}
			}
		}
	}

	now := c.clock()
	var kept []entry
	for _, e := range files {
		if maxIdle > 0 && now.Sub(e.mod) > maxIdle {
			remove(e)
		} else {
			kept = append(kept, e)
		}
	}
	if maxBytes > 0 && total > maxBytes {
		sort.Slice(kept, func(i, j int) bool { return kept[i].mod.Before(kept[j].mod) })
		for _, e := range kept {
			if total <= maxBytes {
				break
			}
			remove(e)
		}
	}
	return removed, nil
}
