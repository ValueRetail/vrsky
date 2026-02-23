#!/bin/bash
# build_example.sh - Compile example.rs to example.wasm
# 
# Prerequisites:
#   - Rust: https://rustup.rs/
#   - wasm-pack: cargo install wasm-pack
#
# Usage:
#   ./build_example.sh
#
# Output: example.wasm (in same directory as this script)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "🔨 Building example.wasm from Rust source..."

# Check for wasm-pack
if ! command -v wasm-pack &> /dev/null; then
    echo "❌ ERROR: wasm-pack not found"
    echo "Install with: cargo install wasm-pack"
    exit 1
fi

# Create Cargo project if it doesn't exist
if [ ! -f "Cargo.toml" ]; then
    echo "📦 Creating Cargo project..."
    cargo init --lib example
    cd example
else
    cd example
fi

# Copy our Rust code into lib.rs
cp ../example.rs src/lib.rs

# Build WASM
echo "🏗️  Compiling to WASM..."
wasm-pack build --target bundler --release

# Copy to testdata
cp pkg/example_bg.wasm ../example.wasm
echo "✓ Successfully compiled example.wasm"
ls -lh ../example.wasm

cd ..
echo "✓ Done! Ready to use example.wasm in tests"
