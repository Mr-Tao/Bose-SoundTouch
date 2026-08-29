package soundtouchweb

import (
	"bytes"
	"io/fs"
	"testing"
)

// TestIndexPolyfillsImportMapsForOlderBrowsers guards the Safari-on-iPadOS-15
// fix (#649): that browser supports native ES modules but not import maps
// (added in Safari 16.4), so the page loaded blank there. es-module-shims
// polyfills import map resolution for such browsers, but is an ~80KB
// uncompressed download, so it must be feature-detected and only injected
// for a browser that actually lacks HTMLScriptElement.supports('importmap') --
// not loaded unconditionally for every browser.
func TestIndexPolyfillsImportMapsForOlderBrowsers(t *testing.T) {
	index, err := fs.ReadFile(StaticFS, "static/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	if !bytes.Contains(index, []byte(`HTMLScriptElement.supports('importmap')`)) {
		t.Fatal("index.html does not feature-detect import map support before loading es-module-shims")
	}

	if bytes.Contains(index, []byte(`document.write(`)) {
		t.Fatal("index.html must not call document.write() (deprecated, subject to browser interventions); use DOM insertion instead")
	}

	shimIdx := bytes.Index(index, []byte(`esModuleShimsScript.src = '/app/static/lib/es-module-shims.js';`))
	if shimIdx == -1 {
		t.Fatal("index.html does not conditionally inject es-module-shims.js")
	}

	importMapIdx := bytes.Index(index, []byte(`<script type="importmap">`))
	if importMapIdx == -1 {
		t.Fatal("index.html does not declare an import map")
	}

	if shimIdx > importMapIdx {
		t.Fatal("the es-module-shims feature-detect/injection must come before the import map so it can polyfill browsers without native support")
	}

	if _, err := fs.Stat(StaticFS, "static/lib/es-module-shims.js"); err != nil {
		t.Errorf("es-module-shims is not vendored/embedded: %v", err)
	}
}

// TestVendoredDependenciesIncludeLicenses ensures that the license and package
// metadata copied with the embedded frontend assets are also present in release
// binaries. scripts/update-static-deps.sh owns these generated files.
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

// TestIndexImportMapCoversAllVendoredModules guards against the import map
// and the vendored files it points at drifting apart.
func TestIndexImportMapCoversAllVendoredModules(t *testing.T) {
	index, err := fs.ReadFile(StaticFS, "static/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	cases := []struct {
		specifier    string
		mappedURL    string
		embeddedPath string
	}{
		{"preact", "/app/static/lib/preact.module.js", "static/lib/preact.module.js"},
		{"preact/hooks", "/app/static/lib/preact-hooks.module.js", "static/lib/preact-hooks.module.js"},
		{"htm", "/app/static/lib/htm.module.js", "static/lib/htm.module.js"},
	}

	for _, c := range cases {
		if !bytes.Contains(index, []byte(`"`+c.specifier+`": "`+c.mappedURL+`"`)) {
			t.Errorf("import map does not map %q to %q", c.specifier, c.mappedURL)
		}

		if _, err := fs.Stat(StaticFS, c.embeddedPath); err != nil {
			t.Errorf("vendored dependency %q: %v", c.specifier, err)
		}
	}
}

// TestPreactHooksUsesUnmodifiedPeerImport guards against re-introducing a
// sed-patched vendor file: with the import map restored, the vendored
// preact/hooks build's peer import of "preact" must stay a bare specifier,
// resolved like every other component through the import map (and, for
// browsers that need it, through the es-module-shims polyfill) rather than
// a hardcoded relative path baked in at vendoring time.
func TestPreactHooksUsesUnmodifiedPeerImport(t *testing.T) {
	hooks, err := fs.ReadFile(StaticFS, "static/lib/preact-hooks.module.js")
	if err != nil {
		t.Fatalf("read preact-hooks.module.js: %v", err)
	}

	if !bytes.Contains(hooks, []byte(`from"preact"`)) {
		t.Error(`preact-hooks.module.js should import the unmodified bare "preact" specifier`)
	}
}
