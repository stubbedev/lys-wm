package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

type source struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Ref  string `json:"ref"`
}

type entryRef struct {
	Package string `json:"package"`
	Source  string `json:"source"`
	Path    string `json:"path"`
}

type manifest struct {
	Sources   []source   `json:"sources"`
	Protocols []entryRef `json:"protocols"`
	dir       string
	sources   map[string]source
}

func loadManifest(dir string) (*manifest, error) {
	path := filepath.Join(dir, "protocols.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	m := &manifest{dir: dir, sources: map[string]source{}}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, s := range m.Sources {
		if s.Name == "" || s.Ref == "" || !strings.Contains(s.URL, "{ref}") || !strings.Contains(s.URL, "{path}") {
			return nil, fmt.Errorf("%s: source %q needs a name, a ref and a url with {ref} and {path}", path, s.Name)
		}
		if _, dup := m.sources[s.Name]; dup {
			return nil, fmt.Errorf("%s: source %q listed twice", path, s.Name)
		}
		m.sources[s.Name] = s
	}
	packages := map[string]bool{}
	for _, p := range m.Protocols {
		if _, ok := m.sources[p.Source]; !ok {
			return nil, fmt.Errorf("%s: protocol %s names unknown source %q", path, p.Path, p.Source)
		}
		if !token.IsIdentifier(p.Package) || strings.ToLower(p.Package) != p.Package || token.IsKeyword(p.Package) {
			return nil, fmt.Errorf("%s: package %q is not a lower-case Go identifier", path, p.Package)
		}
		if packages[p.Package] {
			return nil, fmt.Errorf("%s: package %q listed twice", path, p.Package)
		}
		packages[p.Package] = true
		if filepath.IsAbs(p.Path) || strings.Contains(p.Path, "..") {
			return nil, fmt.Errorf("%s: protocol path %q must be relative to its source", path, p.Path)
		}
	}
	return m, nil
}

func (m *manifest) xmlPath(p entryRef) string {
	return filepath.Join(m.dir, "xml", p.Source, filepath.FromSlash(p.Path))
}

func (m *manifest) url(p entryRef) string {
	s := m.sources[p.Source]
	return strings.NewReplacer("{ref}", s.Ref, "{path}", p.Path).Replace(s.URL)
}
