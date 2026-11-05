package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"
)

const (
	SourceBuiltIn = "built-in"
	SourceConfig  = "config"
	SourceRuntime = "runtime"
)

const RuntimeFile = "runtime.yaml"

const lineStride = 1 << 24

type Layer struct {
	Source string
	File   string
	Data   []byte
}

type layered struct {
	files   []string
	sources []string
}

func (l layered) where(line int) (string, int) {
	return l.files[line/lineStride], line % lineStride
}

func (l layered) source(line int) string {
	return l.sources[line/lineStride]
}

func shift(n *yaml.Node, by int) {
	n.Line += by
	for _, c := range n.Content {
		shift(c, by)
	}
}

func mappingKey(content []*yaml.Node, key string) int {
	for i := 0; i+1 < len(content); i += 2 {
		if content[i].Value == key {
			return i
		}
	}
	return -1
}

func overlay(base, over *yaml.Node) *yaml.Node {
	switch {
	case base == nil || base.Kind == 0:
		return over
	case over.Kind == 0:
		return base
	case base.Kind == yaml.DocumentNode && over.Kind == yaml.DocumentNode:
		if len(base.Content) == 0 {
			return over
		}
		if len(over.Content) == 0 {
			return base
		}
		out := *base
		out.Content = []*yaml.Node{overlay(base.Content[0], over.Content[0])}
		return &out
	case base.Kind != yaml.MappingNode || over.Kind != yaml.MappingNode:
		return over
	}
	out := *base
	out.Content = slices.Clone(base.Content)
	for i := 0; i+1 < len(over.Content); i += 2 {
		key, value := over.Content[i], over.Content[i+1]
		j := mappingKey(out.Content, key.Value)
		if j < 0 {
			out.Content = append(out.Content, key, value)
			continue
		}
		out.Content[j], out.Content[j+1] = key, overlay(out.Content[j+1], value)
	}
	return &out
}

func merged(layers []Layer) (*yaml.Node, layered, error) {
	var doc *yaml.Node
	var l layered
	for i, layer := range layers {
		var n yaml.Node
		if err := yaml.Unmarshal(layer.Data, &n); err != nil {
			return nil, layered{}, fmt.Errorf("%s: %w", layer.File, err)
		}
		shift(&n, i*lineStride)
		l.files, l.sources = append(l.files, layer.File), append(l.sources, layer.Source)
		doc = overlay(doc, &n)
	}
	if doc == nil {
		doc = &yaml.Node{}
	}
	return doc, l, nil
}

var typeErrorLine = regexp.MustCompile(`^line ([0-9]+): `)

func (l layered) decodeError(err error) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return fmt.Errorf("%s: %w", l.files[0], err)
	}
	var files []string
	byFile := map[string][]string{}
	for _, e := range te.Errors {
		file := l.files[0]
		if m := typeErrorLine.FindStringSubmatchIndex(e); m != nil {
			line, convErr := strconv.Atoi(e[m[2]:m[3]])
			if convErr == nil {
				var at int
				file, at = l.where(line)
				e = fmt.Sprintf("line %d: %s", at, e[m[1]:])
			}
		}
		if _, seen := byFile[file]; !seen {
			files = append(files, file)
		}
		byFile[file] = append(byFile[file], e)
	}
	errs := make([]error, 0, len(files))
	for _, file := range files {
		errs = append(errs, fmt.Errorf("%s: %w", file, &yaml.TypeError{Errors: byFile[file]}))
	}
	return errors.Join(errs...)
}

type KeySource struct {
	Path   []string `json:"path"`
	Source string   `json:"source"`
	File   string   `json:"file"`
	Line   int      `json:"line"`
	Value  any      `json:"value"`
}

func Sources(layers ...Layer) ([]KeySource, error) {
	doc, l, err := merged(layers)
	if err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return []KeySource{}, nil
	}
	out := []KeySource{}
	var walk func(n *yaml.Node, key *yaml.Node, path []string) error
	walk = func(n *yaml.Node, key *yaml.Node, path []string) error {
		if n.Kind == yaml.MappingNode && len(n.Content) > 0 {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if err := walk(n.Content[i+1], n.Content[i], append(slices.Clone(path), n.Content[i].Value)); err != nil {
					return err
				}
			}
			return nil
		}
		if key == nil {
			return nil
		}
		var v any
		if err := n.Decode(&v); err != nil {
			return l.decodeError(err)
		}
		file, line := l.where(key.Line)
		out = append(out, KeySource{Path: path, Source: l.source(key.Line), File: file, Line: line, Value: v})
		return nil
	}
	if err := walk(doc.Content[0], nil, nil); err != nil {
		return nil, err
	}
	return out, nil
}
