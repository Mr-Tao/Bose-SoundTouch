#!/bin/bash
set -e

# Directory where libraries are stored
LIB_DIR="pkg/service/soundtouchweb/static/lib"
mkdir -p "$LIB_DIR"

echo "Installing static frontend dependencies from package-lock.json..."
npm ci --ignore-scripts

# Copy files from node_modules
echo "Copying Preact..."
cp node_modules/preact/dist/preact.module.js "$LIB_DIR/preact.module.js"

echo "Copying Preact Hooks..."
cp node_modules/preact/hooks/dist/hooks.module.js "$LIB_DIR/preact-hooks.module.js"
# Browsers without import map support need the peer dependency to use a local URL.
sed -i.bak 's/from"preact"/from".\/preact.module.js"/' "$LIB_DIR/preact-hooks.module.js"
rm -f "$LIB_DIR/preact-hooks.module.js.bak"
if ! grep -q 'from".\/preact.module.js"' "$LIB_DIR/preact-hooks.module.js"; then
    echo "Failed to rewrite the Preact Hooks peer dependency" >&2
    exit 1
fi

echo "Copying HTM..."
cp node_modules/htm/dist/htm.module.js "$LIB_DIR/htm.module.js"

# Polyfills import map support for browsers that have ES modules but not
# import maps (e.g. Safari on iPadOS 15, see #649). Safe to always vendor:
# it detects native import map support and becomes a no-op there.
echo "Copying ES Module Shims..."
cp node_modules/es-module-shims/dist/es-module-shims.js "$LIB_DIR/es-module-shims.js"

# Keep license and package provenance inside the embedded static tree so they
# ship with release binaries, not only with source checkouts.
LICENSE_DIR="$LIB_DIR/LICENSES"
mkdir -p "$LICENSE_DIR"
for dependency in preact htm es-module-shims; do
    cp "node_modules/$dependency/LICENSE" "$LICENSE_DIR/$dependency-LICENSE"
done
cp package-lock.json "$LICENSE_DIR/package-lock.json"

echo "All dependencies updated successfully from node_modules."
