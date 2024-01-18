#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

WASM_PROCESSORS_DIR="$SCRIPT_DIR/wasm_processors"

for dir in "$WASM_PROCESSORS_DIR"/*/; do
    # Check if the directory contains a .go file
    if [ -e "${dir}processor.go" ]; then
        cd "$dir" || exit

        GOOS=wasip1 GOARCH=wasm go build -o processor.wasm processor.go

        cd "$WASM_PROCESSORS_DIR" || exit
    fi
done
