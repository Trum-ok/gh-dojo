package config

import (
	"embed"
	"errors"
	"path"
	"slices"
	"strings"
)

// presetFS holds the presets: same format as .github/dojo.toml, without
// preset and [board].
//
//go:embed presets/*.toml
var presetFS embed.FS

// Presets lists the names of the embedded presets, sorted.
func Presets() []string {
	entries, _ := presetFS.ReadDir("presets")
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".toml"); ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// loadPreset parses the named preset. An unknown name returns an error; a
// broken preset is a bug caught by the package tests.
func loadPreset(name string) (*rawFile, error) {
	if !slices.Contains(Presets(), name) {
		return nil, errUnknownPreset
	}
	data, err := presetFS.ReadFile(path.Join("presets", name+".toml"))
	if err != nil {
		return nil, err
	}
	return parseRaw(data, "preset "+name, true)
}

var errUnknownPreset = errors.New("unknown preset")
