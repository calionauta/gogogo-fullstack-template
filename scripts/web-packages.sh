#!/usr/bin/env bash
# Lists the package directories that comprise the deployable web application
# and its tests. Directories work with Go tools and golangci-lint alike.
# cmd/desktop is a separate Wails/CGO target with OS-native WebView headers;
# it is validated by .github/workflows/desktop.yml instead of web CI.

set -euo pipefail

project_dir=$(pwd)
go list -f '{{.Dir}}' ./... | grep -vE "/cmd/desktop$|^${project_dir}$"
