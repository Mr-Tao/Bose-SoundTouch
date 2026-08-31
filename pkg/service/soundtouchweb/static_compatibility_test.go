package soundtouchweb

import (
	"bytes"
	"io/fs"
	"regexp"
	"testing"
)

func TestStaticModulesSupportLegacyImportMapBrowsers(t *testing.T) {
	index, err := fs.ReadFile(StaticFS, "static/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	if !bytes.Contains(index, []byte(`HTMLScriptElement.supports('importmap')`)) {
		t.Fatal("index.html does not feature-detect import map support before loading es-module-shims")
	}
	if bytes.Contains(index, []byte(`document.write(`)) {
		t.Fatal("index.html must use DOM insertion instead of document.write()")
	}
	if !bytes.Contains(index, []byte(`.async = false`)) {
		t.Fatal("the dynamically inserted es-module-shims script must preserve execution order")
	}

	shimIndex := bytes.Index(index, []byte(`esModuleShimsScript.src = '/app/static/lib/es-module-shims.js';`))
	importMapIndex := bytes.Index(index, []byte(`<script type="importmap">`))
	if shimIndex == -1 || importMapIndex == -1 || shimIndex > importMapIndex {
		t.Fatal("the conditional es-module-shims loader must precede the import map")
	}

	dependencies := map[string]string{
		"preact":       "static/lib/preact.module.js",
		"preact/hooks": "static/lib/preact-hooks.module.js",
		"htm":          "static/lib/htm.module.js",
	}
	for specifier, embeddedPath := range dependencies {
		mappedURL := "/app/" + embeddedPath
		if !bytes.Contains(index, []byte(`"`+specifier+`": "`+mappedURL+`"`)) {
			t.Errorf("import map does not map %q to %q", specifier, mappedURL)
		}
		if _, err := fs.Stat(StaticFS, embeddedPath); err != nil {
			t.Errorf("vendored dependency %q: %v", specifier, err)
		}
	}
	if _, err := fs.Stat(StaticFS, "static/lib/es-module-shims.js"); err != nil {
		t.Errorf("es-module-shims is not vendored/embedded: %v", err)
	}

	dependencyModule, err := fs.ReadFile(StaticFS, "static/js/dependencies.js")
	if err != nil {
		t.Fatalf("read dependency module: %v", err)
	}
	for _, modulePath := range []string{
		"../lib/preact.module.js",
		"../lib/preact-hooks.module.js",
		"../lib/htm.module.js",
	} {
		if !bytes.Contains(dependencyModule, []byte(modulePath)) {
			t.Errorf("dependency module does not import %q", modulePath)
		}
	}

	bareImport := regexp.MustCompile(`\bfrom\s*['"]([^./][^'"]*)['"]`)
	err = fs.WalkDir(StaticFS, "static/js", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || bytes.HasSuffix([]byte(path), []byte(".test.mjs")) ||
			(!bytes.HasSuffix([]byte(path), []byte(".js")) &&
				!bytes.HasSuffix([]byte(path), []byte(".mjs"))) {
			return nil
		}

		source, err := fs.ReadFile(StaticFS, path)
		if err != nil {
			return err
		}
		for _, match := range bareImport.FindAllSubmatch(source, -1) {
			if _, ok := dependencies[string(match[1])]; !ok {
				t.Errorf("%s contains unmapped bare dependency import %q", path, match[1])
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walk JavaScript modules: %v", err)
	}
}

func TestVendoredDependenciesIncludeLicenses(t *testing.T) {
	for _, dependency := range []string{"preact", "htm", "es-module-shims"} {
		path := "static/lib/LICENSES/" + dependency + "-LICENSE"
		if _, err := fs.Stat(StaticFS, path); err != nil {
			t.Errorf("vendored dependency license %q: %v", path, err)
		}
	}

	if _, err := fs.Stat(StaticFS, "static/lib/LICENSES/package-lock.json"); err != nil {
		t.Errorf("vendored dependency provenance: %v", err)
	}
}
