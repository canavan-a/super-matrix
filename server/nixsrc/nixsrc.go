// Package nixsrc reads a flake's directory into the files a Nix service
// keeps inline: what supermatrix-admin's source: and supermatrix-relay
// nix --dir send.
package nixsrc

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxBytes bounds a directory's files, as the server does.
const MaxBytes = 1 << 20

// skipDirs are never read.
var skipDirs = map[string]bool{".git": true, ".direnv": true, "node_modules": true}

// ReadDir reads a flake's directory: every regular file but those in
// .git, .direnv, node_modules, symlinks (like nix's result) and what
// .supermatrixignore lists (one glob per line, matched against the path
// and the name).
func ReadDir(dir string) (map[string]string, error) {
	var ignore []string
	if b, err := os.ReadFile(filepath.Join(dir, ".supermatrixignore")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				ignore = append(ignore, strings.TrimSuffix(l, "/"))
			}
		}
	}
	ignored := func(rel string) bool {
		for _, g := range ignore {
			if ok, _ := filepath.Match(g, rel); ok {
				return true
			}
			if ok, _ := filepath.Match(g, filepath.Base(rel)); ok {
				return true
			}
		}
		return false
	}
	out := map[string]string{}
	total := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if skipDirs[d.Name()] || ignored(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || rel == ".supermatrixignore" || ignored(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		total += len(b) + len(rel)
		if total > MaxBytes {
			return fmt.Errorf("%s comes to more than 1 MiB (list what to leave out in .supermatrixignore)", dir)
		}
		out[rel] = string(b)
		return nil
	})
	return out, err
}
