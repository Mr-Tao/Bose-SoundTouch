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

	const dependencyModulePath = "static/js/dependencies.js"
	dependencyModule, err := fs.ReadFile(StaticFS, dependencyModulePath)
	if err != nil {
		t.Fatalf("read dependency module: %v", err)
	}

	dependencies := map[string]string{
		"../lib/preact.module.js":       "static/lib/preact.module.js",
		"../lib/preact-hooks.module.js": "static/lib/preact-hooks.module.js",
		"../lib/htm.module.js":          "static/lib/htm.module.js",
	}
	for modulePath, embeddedPath := range dependencies {
		if _, err := fs.Stat(StaticFS, embeddedPath); err != nil {
			t.Errorf("static dependency %q: %v", modulePath, err)
		}
		if !bytes.Contains(dependencyModule, []byte(modulePath)) {
			t.Errorf("dependency module does not import %q", modulePath)
		}
	}

	bareDependency := regexp.MustCompile(`\bfrom\s*['"](?:preact(?:/hooks)?|htm)['"]`)
	directDependency := regexp.MustCompile(`\bfrom\s*['"][^'"]*lib/(?:preact(?:-hooks)?|htm)\.module\.js['"]`)
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
		if path != dependencyModulePath && strings.HasPrefix(path, "static/js/") && directDependency.Match(source) {
			t.Errorf("%s bypasses %s", path, dependencyModulePath)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk JavaScript modules: %v", err)
	}
}
