#!/bin/bash
set -e

# Directory where libraries are stored
LIB_DIR="pkg/service/soundtouchweb/static/lib"
mkdir -p "$LIB_DIR"

echo "Updating static frontend dependencies from node_modules..."

# Ensure dependencies are installed
if [ ! -d "node_modules" ] || [ ! -d "node_modules/preact" ] || [ ! -d "node_modules/htm" ] || [ ! -d "node_modules/es-module-shims" ]; then
    if [ "$CI" = "true" ]; then
        echo "node_modules not found or incomplete. Running npm ci in CI environment..."
        npm ci
    else
        echo "node_modules not found or incomplete. Running npm install..."
        npm install
    fi
fi

# Copy files from node_modules
echo "Copying Preact..."
cp node_modules/preact/dist/preact.module.js "$LIB_DIR/preact.module.js"

echo "Copying Preact Hooks..."
cp node_modules/preact/hooks/dist/hooks.module.js "$LIB_DIR/preact-hooks.module.js"

echo "Copying HTM..."
cp node_modules/htm/dist/htm.module.js "$LIB_DIR/htm.module.js"

# Polyfills import map support for browsers that have ES modules but not
# import maps (e.g. Safari on iPadOS 15, see #649). Safe to always vendor:
# it detects native import map support and becomes a no-op there.
echo "Copying ES Module Shims..."
cp node_modules/es-module-shims/dist/es-module-shims.js "$LIB_DIR/es-module-shims.js"

echo "All dependencies updated successfully from node_modules."
