package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// envFromDir applies an environment directory to base, by the rules of
// chpst -e and envdir: every file sets the variable named after it to the
// first line of its content, with trailing spaces and tabs removed and NUL
// bytes turned into newlines. An empty file removes the variable. Entries
// whose name starts with a dot, and directories, are skipped.
func envFromDir(base []string, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	set := map[string]string{}
	unset := map[string]bool{}
	var order []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		if fi, err := os.Stat(path); err == nil && fi.IsDir() {
			continue
		}
		if strings.Contains(name, "=") {
			return nil, fmt.Errorf("%s: name must not contain '='", path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(b) == 0 {
			unset[name] = true
			continue
		}
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[:i]
		}
		b = bytes.TrimRight(b, " \t")
		b = bytes.ReplaceAll(b, []byte{0}, []byte{'\n'})
		set[name] = string(b)
		order = append(order, name)
	}

	env := make([]string, 0, len(base)+len(order))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := set[name]; ok || unset[name] {
			continue
		}
		env = append(env, kv)
	}
	for _, name := range order {
		env = append(env, name+"="+set[name])
	}
	return env, nil
}
