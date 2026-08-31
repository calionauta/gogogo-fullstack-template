#!/usr/bin/env bash
# desktop-build.sh — Build the gogogo-fullstack-template as a native
# desktop app (Wails v3) or Android APK.
#
# Usage:
#   ./scripts/desktop-build.sh              # build for current platform
#   ./scripts/desktop-build.sh android      # build Android APK
#   ./scripts/desktop-build.sh ios          # (future) build iOS app
#   ./scripts/desktop-build.sh package      # macOS .app bundle
#
# Prerequisites (checked automatically):
#   - Go 1.26+
#   - Wails v3 CLI (go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.12)
#   - Android: SDK API 35 + NDK 26.3.x + JDK 21 (for Android builds)
#   - macOS: Xcode Command Line Tools (for .app packaging)
#
# The desktop binary shares 100% of the backend code (PocketBase + goqite
# + router + handlers). With NATS_LEAFNODE_URL set, it becomes a NATS
# Leaf Node that syncs JetStream with the central server (offline edits
# replay on reconnect).
#
# Build output:
#   build/desktop/          — platform-specific binary
#   build/android/          — .apk (when target is android)
#   build/package/          — .app bundle (macOS, when target is package)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_DIR="$PROJECT_DIR/build"
APP_NAME="${APP_NAME:-gogogo-fullstack-template}"
TARGET="${1:-native}"

# ── Color helpers ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color
info()  { echo -e "${GREEN}→${NC} $1"; }
warn()  { echo -e "${YELLOW}⚠${NC} $1"; }
error() { echo -e "${RED}✗${NC} $1"; }

# ── Prerequisite checks ──
check_go() {
    if ! command -v go &>/dev/null; then
        error "Go is not installed. Install Go 1.26+ from https://go.dev/dl/"
        return 1
    fi
    local version
    version=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+')
    if awk "BEGIN {exit !($version < 1.26)}"; then
        error "Go $version detected. Go 1.26+ required."
        return 1
    fi
    info "Go $version detected"
}

check_wails() {
    if ! command -v wails3 &>/dev/null && ! command -v wails &>/dev/null; then
        warn "Wails CLI not found. Installing..."
        go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.12
        if ! command -v wails3 &>/dev/null; then
            error "Wails installation failed. Try: go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.12"
            return 1
        fi
    fi
    local cmd
    cmd=$(command -v wails3 2>/dev/null || command -v wails 2>/dev/null)
    info "Wails CLI: $cmd"
}

check_android() {
    if [ -z "${ANDROID_HOME:-}" ] && [ -z "${ANDROID_SDK_ROOT:-}" ]; then
        error "ANDROID_HOME or ANDROID_SDK_ROOT not set."
        error "Install Android SDK + NDK, then: export ANDROID_HOME=~/Android/Sdk"
        return 1
    fi
    local sdk="${ANDROID_HOME:-$ANDROID_SDK_ROOT}"
    if [ ! -d "$sdk" ]; then
        error "Android SDK directory not found: $sdk"
        return 1
    fi
    info "Android SDK: $sdk"

    if ! command -v java &>/dev/null; then
        error "Java (JDK 21) not found. Install via: brew install openjdk@21"
        return 1
    fi
    local java_version
    java_version=$(java -version 2>&1 | grep -oP 'version "\K[0-9]+')
    if [ "$java_version" -lt 21 ]; then
        error "Java $java_version detected. JDK 21+ required."
        return 1
    fi
    info "Java $java_version detected"
}

check_macos_package() {
    if [ "$(uname)" != "Darwin" ]; then
        error "macOS .app packaging is only available on macOS."
        return 1
    fi
    if ! xcode-select -p &>/dev/null; then
        error "Xcode Command Line Tools not installed. Run: xcode-select --install"
        return 1
    fi
    info "Xcode Command Line Tools detected"
}

# ── Build functions ──
build_native() {
    info "Building desktop binary for $(go env GOOS)/$(go env GOARCH)..."
    mkdir -p "$OUTPUT_DIR/desktop"
    cd "$PROJECT_DIR"

    # First generate Templ components
    go tool templ generate

    # Build with Wails or plain Go
    if command -v wails3 &>/dev/null; then
        wails3 build -o "$OUTPUT_DIR/desktop/$APP_NAME"
    elif command -v wails &>/dev/null; then
        wails build -o "$OUTPUT_DIR/desktop/$APP_NAME"
    else
        go build -o "$OUTPUT_DIR/desktop/$APP_NAME" ./cmd/desktop
    fi
    info "Binary: $OUTPUT_DIR/desktop/$APP_NAME"
}

build_android() {
    info "Building Android APK..."
    check_android || return 1
    mkdir -p "$OUTPUT_DIR/android"
    cd "$PROJECT_DIR"

    go tool templ generate
    wails3 android:package -o "$OUTPUT_DIR/android/$APP_NAME.apk"
    info "APK: $OUTPUT_DIR/android/$APP_NAME.apk"
}

build_ios() {
    info "iOS build not yet supported in this template."
    info "Wails v3 iOS support is experimental. Track: https://github.com/wailsapp/wails"
    return 1
}

build_package() {
    info "Building macOS .app bundle..."
    check_macos_package || return 1
    mkdir -p "$OUTPUT_DIR/package"
    cd "$PROJECT_DIR"

    go tool templ generate
    wails3 package GOOS=darwin -o "$OUTPUT_DIR/package"
    info ".app bundle: $OUTPUT_DIR/package"
}

# ── Main ──
echo ""
echo "╔══════════════════════════════════════════╗"
echo "║  gogogo — Desktop Build Script          ║"
echo "╚══════════════════════════════════════════╝"
echo ""

check_go || exit 1

case "$TARGET" in
    native)
        check_wails || true  # wails is optional for native build
        build_native
        ;;
    android)
        check_wails
        build_android
        ;;
    ios)
        build_ios
        ;;
    package)
        check_wails
        build_package
        ;;
    *)
        echo "Usage: $0 [native|android|package]"
        echo ""
        echo "  native   — build for current OS/arch (default)"
        echo "  android  — build Android APK (requires SDK + NDK + JDK 21)"
        echo "  package  — build macOS .app bundle (macOS only)"
        exit 1
        ;;
esac

echo ""
info "Build complete! Output in: $OUTPUT_DIR/$TARGET"
