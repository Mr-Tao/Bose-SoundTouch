package soundtouchweb

import (
	"bytes"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestStaticModulesDoNotRequireImportMaps(t *testing.T) {
	index, err := fs.ReadFile(StaticFS, "static/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if bytes.Contains(index, []byte(`type="importmap"`)) {
		t.Fatal("index.html must not require import map support")
	}

	dependencies := map[string]string{
		"/app/static/lib/preact.module.js":       "static/lib/preact.module.js",
		"/app/static/lib/preact-hooks.module.js": "static/lib/preact-hooks.module.js",
		"/app/static/lib/htm.module.js":          "static/lib/htm.module.js",
	}
	usedDependencies := make(map[string]bool, len(dependencies))
	for publicPath, embeddedPath := range dependencies {
		if _, err := fs.Stat(StaticFS, embeddedPath); err != nil {
			t.Errorf("static dependency %q: %v", publicPath, err)
		}
	}

	bareDependency := regexp.MustCompile(`\bfrom\s*['"](?:preact(?:/hooks)?|htm)['"]`)
	err = fs.WalkDir(StaticFS, "static", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".js") {
			return nil
		}

		source, err := fs.ReadFile(StaticFS, path)
		if err != nil {
			return err
		}
		if bareDependency.Match(source) {
			t.Errorf("%s contains a bare dependency import", path)
		}
		for publicPath := range dependencies {
			if bytes.Contains(source, []byte(publicPath)) {
				usedDependencies[publicPath] = true
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk JavaScript modules: %v", err)
	}

	for publicPath := range dependencies {
		if !usedDependencies[publicPath] {
			t.Errorf("static dependency %q is not imported", publicPath)
		}
	}
}
