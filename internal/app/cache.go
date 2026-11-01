package app

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/Will-Luck/waymark/internal/registry"
)

// Cache files in DATA_DIR hold the last good bytes of the projects file and
// ideas.json, so a restart while the source is down still shows projects.
const (
	projectsCache = "projects.cache.yaml"
	ideasCache    = "ideas.cache.json"
)

// readCaches returns the cached projects and ideas for the sources that are
// configured, or the empty ones when a cache is missing, no longer parses or
// its source is not configured (a stale cache must not show).
func readCaches(dir string, projects, ideasSet bool, forgeIDs []string) (*registry.Registry, *registry.Ideas) {
	reg, ideas := registry.Empty(), &registry.Ideas{}
	if projects {
		b, err := os.ReadFile(filepath.Join(dir, projectsCache))
		if err != nil && !os.IsNotExist(err) {
			log.Printf("cache %s unreadable: %v", projectsCache, err)
		}
		if err == nil {
			if r, err := registry.Parse(b, forgeIDs); err == nil {
				reg = r
			} else {
				log.Printf("cache %s ignored: %v", projectsCache, err)
			}
		}
	}
	if !ideasSet {
		return reg, ideas
	}
	b, err := os.ReadFile(filepath.Join(dir, ideasCache))
	if err != nil && !os.IsNotExist(err) {
		log.Printf("cache %s unreadable: %v", ideasCache, err)
	}
	if err == nil {
		if i, err := registry.ParseIdeas(b); err == nil {
			ideas = i
		} else {
			log.Printf("cache %s ignored: %v", ideasCache, err)
		}
	}
	return reg, ideas
}

// writeFileAtomic writes beside the target and renames, so a reader (or a
// crash) never sees half a file.
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := firstErr(werr, serr, cerr); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
