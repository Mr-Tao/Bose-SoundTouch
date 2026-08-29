package soundtouchweb

import (
	"bytes"
	"io/fs"
	"testing"
)

// TestIndexPolyfillsImportMapsForOlderBrowsers guards the Safari-on-iPadOS-15
// fix (#649): that browser supports native ES modules but not import maps
// (added in Safari 16.4), so the page loaded blank there. es-module-shims
// polyfills import map resolution for such browsers and detects/no-ops on
// every browser with native support, so it must be loaded -- with the
// "async" attribute -- before the import map is parsed.
func TestIndexPolyfillsImportMapsForOlderBrowsers(t *testing.T) {
	index, err := fs.ReadFile(StaticFS, "static/index.html")
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	shimIdx := bytes.Index(index, []byte(`<script async src="/app/static/lib/es-module-shims.js"></script>`))
	if shimIdx == -1 {
		t.Fatal(`index.html does not load es-module-shims.js with the "async" attribute`)
	}

	importMapIdx := bytes.Index(index, []byte(`<script type="importmap">`))
	if importMapIdx == -1 {
		t.Fatal("index.html does not declare an import map")
	}

	if shimIdx > importMapIdx {
		t.Fatal("es-module-shims must be loaded before the import map so it can polyfill browsers without native support")
	}

	if _, err := fs.Stat(StaticFS, "static/lib/es-module-shims.js"); err != nil {
		t.Errorf("es-module-shims is not vendored/embedded: %v", err)
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
