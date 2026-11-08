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

type entryRef struct {
	Package string `json:"package"`
	File    string `json:"file"`
}

type manifest struct {
	Protocols []entryRef `json:"protocols"`
	dir       string
}

func loadManifest(dir string) (*manifest, error) {
	path := filepath.Join(dir, "protocols.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	m := &manifest{dir: dir}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	packages := map[string]bool{}
	for _, p := range m.Protocols {
		if !token.IsIdentifier(p.Package) || strings.ToLower(p.Package) != p.Package || token.IsKeyword(p.Package) {
			return nil, fmt.Errorf("%s: package %q is not a lower-case Go identifier", path, p.Package)
		}
		if packages[p.Package] {
			return nil, fmt.Errorf("%s: package %q listed twice", path, p.Package)
		}
		packages[p.Package] = true
		if !strings.HasSuffix(p.File, ".xml") || strings.ContainsRune(p.File, '/') {
			return nil, fmt.Errorf("%s: protocol file %q is not a file name in lys-lib's protocols", path, p.File)
		}
	}
	return m, nil
}
