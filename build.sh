#!/bin/bash
set -euo pipefail

OUTPUT_DIR="output"
BINARY_NAME="notification-server"
MAIN_PACKAGE="./cmd/notification-server/"

mkdir -p "${OUTPUT_DIR}"

go build -o "${OUTPUT_DIR}/${BINARY_NAME}" "${MAIN_PACKAGE}"

echo "Build complete: ${OUTPUT_DIR}/${BINARY_NAME}"
