#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
go run "$dir/testplugin/main.go"