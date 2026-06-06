package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---- YAML template walker ----

// yamlSeg is a parsed segment of a path template.
type yamlSeg struct {
	isVar   bool
	name    string
	fileExt string
}

// parseYAMLTemplate parses a template like "{biz}/routes/{event}.yaml".
func parseYAMLTemplate(tmpl string) []yamlSeg {
	parts := strings.Split(tmpl, "/")
	segs := make([]yamlSeg, len(parts))
	for i, part := range parts {
		if brace := strings.IndexByte(part, '{'); brace >= 0 {
			closeB := strings.IndexByte(part, '}')
			segs[i] = yamlSeg{isVar: true, name: part[brace+1 : closeB], fileExt: part[closeB+1:]}
		} else {
			segs[i] = yamlSeg{isVar: false, name: part}
		}
	}
	return segs
}

// walkYAML walks a path template relative to rootDir, finds all matching
// .yaml files, parses each into T, and calls fn for each.
func walkYAML[T any](rootDir, tmpl string, fn func(T, string, map[string]string, error)) {
	segs := parseYAMLTemplate(tmpl)
	walkYAMLAt[T](rootDir, segs, 0, map[string]string{}, fn)
}

func walkYAMLAt[T any](dir string, segs []yamlSeg, idx int,
	vars map[string]string, fn func(T, string, map[string]string, error)) {
	if idx >= len(segs) {
		return
	}
	seg := segs[idx]
	isLast := idx == len(segs)-1

	if isLast {
		if seg.isVar {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), seg.fileExt) {
					continue
				}
				v := copyMap(vars)
				v[seg.name] = strings.TrimSuffix(e.Name(), seg.fileExt)
				parseYAMLFileAt[T](filepath.Join(dir, e.Name()), v, fn)
			}
		} else {
			parseYAMLFileAt[T](filepath.Join(dir, seg.name), vars, fn)
		}
		return
	}

	if seg.isVar {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := copyMap(vars)
			v[seg.name] = e.Name()
			walkYAMLAt[T](filepath.Join(dir, e.Name()), segs, idx+1, v, fn)
		}
	} else {
		subDir := filepath.Join(dir, seg.name)
		if !existsAndIsDir(subDir) {
			return
		}
		walkYAMLAt[T](subDir, segs, idx+1, vars, fn)
	}
}

func parseYAMLFileAt[T any](path string, vars map[string]string, fn func(T, string, map[string]string, error)) {
	var val T
	data, err := os.ReadFile(path)
	if err != nil {
		var zero T
		fn(zero, path, vars, fmt.Errorf("reading file: %w", err))
		return
	}
	if err := yaml.Unmarshal(data, &val); err != nil {
		var zero T
		fn(zero, path, vars, fmt.Errorf("parsing YAML: %w", err))
		return
	}
	fn(val, path, vars, nil)
}

func copyMap(m map[string]string) map[string]string {
	r := make(map[string]string, len(m))
	for k, v := range m {
		r[k] = v
	}
	return r
}
