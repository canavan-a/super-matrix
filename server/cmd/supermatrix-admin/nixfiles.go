package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"main/nixsrc"
)

// File references in a document's nix_services, which only the CLI can
// read (the server never sees your files):
//
//	source: ./nix/kuma              a directory: flake.nix, flake.lock and every other file
//	flake: {file: ./kuma/flake.nix}  one file, for flake, flake_lock or an entry of files
//
// Paths are relative to the document. They're read and put inline before
// the document is sent; a document without any is sent as it is.

// resolveNixFiles inlines doc's file references, reading them relative to
// base. Outside base only with allowOutside.
func resolveNixFiles(doc []byte, base string, allowOutside bool) ([]byte, bool, error) {
	js, err := yaml.YAMLToJSON(doc)
	if err != nil {
		return doc, false, nil // the server says what's wrong with it
	}
	var root map[string]any
	if json.Unmarshal(js, &root) != nil {
		return doc, false, nil
	}
	services, _ := root["nix_services"].(map[string]any)
	if len(services) == 0 {
		return doc, false, nil
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return nil, false, err
	}
	inside := func(p string) (string, error) {
		full := p
		if !filepath.IsAbs(p) {
			full = filepath.Join(absBase, p)
		}
		full = filepath.Clean(full)
		if !allowOutside {
			rel, err := filepath.Rel(absBase, full)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				return "", fmt.Errorf("%s is outside the document's directory (pass -allow-outside to read it anyway)", p)
			}
		}
		return full, nil
	}
	readRef := func(at string, v any) (any, bool, error) {
		m, ok := v.(map[string]any)
		if !ok {
			return v, false, nil
		}
		p, ok := m["file"].(string)
		if !ok || len(m) != 1 {
			return v, false, nil
		}
		full, err := inside(p)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", at, err)
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", at, err)
		}
		return string(b), true, nil
	}

	changed := false
	keys := make([]string, 0, len(services))
	for k := range services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		svc, ok := services[key].(map[string]any)
		if !ok {
			continue
		}
		at := "nix_services." + key
		for _, f := range []string{"flake", "flake_lock"} {
			if v, ok := svc[f]; ok {
				nv, did, err := readRef(at+"."+f, v)
				if err != nil {
					return nil, false, err
				}
				if did {
					svc[f], changed = nv, true
				}
			}
		}
		if files, ok := svc["files"].(map[string]any); ok {
			for name, v := range files {
				nv, did, err := readRef(at+".files."+name, v)
				if err != nil {
					return nil, false, err
				}
				if did {
					files[name], changed = nv, true
				}
			}
		}
		src, ok := svc["source"].(string)
		if !ok {
			continue
		}
		dir, err := inside(src)
		if err != nil {
			return nil, false, fmt.Errorf("%s.source: %w", at, err)
		}
		read, err := nixsrc.ReadDir(dir)
		if err != nil {
			return nil, false, fmt.Errorf("%s.source: %w", at, err)
		}
		flake, ok := read["flake.nix"]
		if !ok {
			return nil, false, fmt.Errorf("%s.source: there's no flake.nix in %s", at, src)
		}
		if _, given := svc["flake"]; given {
			return nil, false, fmt.Errorf("%s: give flake or source, not both", at)
		}
		svc["flake"] = flake
		delete(read, "flake.nix")
		if lock, ok := read["flake.lock"]; ok {
			if _, given := svc["flake_lock"]; !given {
				svc["flake_lock"] = lock
			}
			delete(read, "flake.lock")
		}
		files, _ := svc["files"].(map[string]any)
		if files == nil {
			files = map[string]any{}
		}
		for name, c := range read {
			if _, given := files[name]; given {
				return nil, false, fmt.Errorf("%s: %s is in source and in files", at, name)
			}
			files[name] = c
		}
		if len(files) > 0 {
			svc["files"] = files
		}
		delete(svc, "source")
		changed = true
	}
	if !changed {
		return doc, false, nil
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, false, err
	}
	y, err := yaml.JSONToYAML(out)
	if err != nil {
		return out, true, nil
	}
	return y, true, nil
}

// splitExport writes each Nix service of an exported document (JSON) to
// dir/<slug>/ and points the document at it with source:, relative to
// docDir (where the document will be saved).
func splitExport(js []byte, dir, docDir string) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(js, &root); err != nil {
		return nil, err
	}
	services, _ := root["nix_services"].(map[string]any)
	for slug, v := range services {
		svc, ok := v.(map[string]any)
		if !ok {
			continue
		}
		sd := filepath.Join(dir, slug)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			return nil, err
		}
		write := func(name string, content any) error {
			s, _ := content.(string)
			full := filepath.Join(sd, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			return os.WriteFile(full, []byte(s), 0o644)
		}
		if err := write("flake.nix", svc["flake"]); err != nil {
			return nil, err
		}
		if lock, ok := svc["flake_lock"]; ok {
			if err := write("flake.lock", lock); err != nil {
				return nil, err
			}
		}
		if files, ok := svc["files"].(map[string]any); ok {
			for name, c := range files {
				if err := write(name, c); err != nil {
					return nil, err
				}
			}
		}
		delete(svc, "flake")
		delete(svc, "flake_lock")
		delete(svc, "files")
		rel, err := filepath.Rel(docDir, sd)
		if err != nil {
			rel = sd
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, ".") && !strings.HasPrefix(rel, "/") {
			rel = "./" + rel
		}
		svc["source"] = rel
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(out)
}

type nixStatusRow struct {
	ID, Slug, RelaySlug, Phase, Desired, LastError string
	Removing, Failing                              bool
	MissingEnv                                     []string
	Progress                                       *struct {
		Step           string
		Built, ToBuild int
		Current        string
	}
}

func (c *schemaClient) nixStatus() ([]nixStatusRow, error) {
	out, err := c.do("GET", "/v1/schema/nix-status", nil, nil)
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == 404 {
			return nil, errors.New("this server doesn't run Nix services yet: update it")
		}
		return nil, err
	}
	var rows []nixStatusRow
	err = json.Unmarshal(out, &rows)
	return rows, err
}

func printNixStatus(w io.Writer, rows []nixStatusRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No Nix services are applied to any relay.")
		return
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Slug+"@"+rows[i].RelaySlug < rows[j].Slug+"@"+rows[j].RelaySlug
	})
	width := 0
	for _, r := range rows {
		width = max(width, len(r.Slug)+1+len(r.RelaySlug))
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%-*s  %s\n", width, r.Slug+"@"+r.RelaySlug, describeNix(r))
	}
}

func describeNix(r nixStatusRow) string {
	s := r.Phase
	switch {
	case r.Progress != nil && r.Progress.ToBuild > 0:
		s += fmt.Sprintf(" %d/%d", r.Progress.Built, r.Progress.ToBuild)
		if r.Progress.Current != "" {
			s += " · " + r.Progress.Current
		}
	case len(r.MissingEnv) > 0:
		s += ": set " + strings.Join(r.MissingEnv, ", ") + " in /etc/supermatrix/nix-services/" + r.Slug + ".env on " + r.RelaySlug
	case r.LastError != "":
		s += ": " + r.LastError
	}
	return s
}

// settled reports whether an applied service is done changing: where
// it's meant to be, or failed.
func settled(r nixStatusRow) (done, failed bool) {
	switch {
	case r.Failing:
		return true, true
	case r.Removing:
		return false, false
	case r.Desired == "stopped":
		return r.Phase == "stopped", false
	}
	return r.Phase == "running", false
}

// waitNix follows the Nix services until each is running (or stopped, as
// asked) or failed; it fails if any failed or time ran out.
func (c *schemaClient) waitNix(w io.Writer, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last := map[string]string{}
	for {
		rows, err := c.nixStatus()
		if err != nil {
			return err
		}
		pending, failed := 0, 0
		for _, r := range rows {
			key := r.Slug + "@" + r.RelaySlug
			if d := describeNix(r); last[key] != d {
				last[key] = d
				fmt.Fprintf(w, "  %s  %s\n", key, d)
			}
			done, bad := settled(r)
			if bad {
				failed++
			} else if !done {
				pending++
			}
		}
		if pending == 0 {
			if failed > 0 {
				return fmt.Errorf("%d Nix service(s) failed", failed)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d Nix service(s) still not running after %s", pending, timeout)
		}
		time.Sleep(2 * time.Second)
	}
}
