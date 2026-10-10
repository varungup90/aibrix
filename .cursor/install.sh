#!/usr/bin/env bash
# Idempotent Cloud Agent setup for AIBrix.
#
# AIBrix has two development surfaces:
#   * a Go control plane (controllers, gateway plugins, console, kvcache-watcher)
#   * a Python runtime under python/aibrix (managed with Poetry)
#
# This script prepares both. It is safe to run repeatedly.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "==> Installing system build dependencies"
# libzmq3-dev / libsodium-dev: required to build the gateway-plugins binary with
#   CGO ZMQ support (make build-gateway-plugins).
# python3-dev + build-essential: required to compile Python C-extensions
#   (e.g. xxhash) that have no prebuilt wheels for this interpreter.
sudo apt-get update
sudo apt-get install -y --no-install-recommends \
    build-essential \
    pkg-config \
    curl \
    libzmq3-dev \
    libsodium-dev \
    python3-dev

echo "==> Downloading Go modules"
go mod download

echo "==> Warming Go build cache (all packages, nozmq default path)"
go build -tags=nozmq ./...

echo "==> Ensuring Poetry is installed"
if [ ! -x "$HOME/.local/bin/poetry" ]; then
    curl -sSL https://install.python-poetry.org | python3 -
fi
# Expose poetry on the default PATH for interactive agent shells.
sudo ln -sf "$HOME/.local/bin/poetry" /usr/local/bin/poetry

echo "==> Installing Python (python/aibrix) dependencies via Poetry"
cd "$REPO_ROOT/python/aibrix"
poetry env use python3.12
poetry install --no-root --with dev,profiling

echo "==> AIBrix environment setup complete"
