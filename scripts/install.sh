#!/bin/bash

set -e

# Function to check if a command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Function to get latest release information
get_latest_release() {
    curl --silent "https://api.github.com/repos/ConduitIO/conduit/releases/latest" | 
    grep '"tag_name":' |
    sed -E 's/.*"([^"]+)".*/\1/'
}

# Function to get system architecture
get_arch() {
    arch=$(uname -m)
    case $arch in
        x86_64)
            echo "x86_64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            echo "Unsupported architecture: $arch" >&2
            exit 1
            ;;
    esac
}

# Detect OS
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS
    if command_exists brew; then
        echo "Installing Conduit using Homebrew..."
        brew install conduit
    else
        echo "Homebrew is not installed. Please install Homebrew first: https://brew.sh/"
        exit 1
    fi
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    # Linux
    if command_exists apt-get; then
        # Debian-based
        echo "Detected Debian-based system. Installing Conduit..."
        ARCH=$(get_arch)
        VERSION=$(get_latest_release)
        URL="https://conduit.gateway.scarf.sh/conduit/download/${VERSION}/conduit_${VERSION#v}_Linux_${ARCH}.deb"
        
        TEMP_DEB="$(mktemp)"
        wget -O "$TEMP_DEB" "$URL"
        sudo dpkg -i "$TEMP_DEB"
        rm -f "$TEMP_DEB"
    elif command_exists rpm; then
        # RPM-based
        echo "Detected RPM-based system. Installing Conduit..."
        ARCH=$(get_arch)
        VERSION=$(get_latest_release)
        URL="https://conduit.gateway.scarf.sh/conduit/download/${VERSION}/conduit_${VERSION#v}_Linux_${ARCH}.rpm"
        
        TEMP_RPM="$(mktemp)"
        wget -O "$TEMP_RPM" "$URL"
        sudo rpm -i "$TEMP_RPM"
        rm -f "$TEMP_RPM"
    else
        echo "Unsupported Linux distribution. Please install manually."
        exit 1
    fi
else
    echo "Unsupported operating system: $OSTYPE"
    exit 1
fi

echo "Conduit has been installed successfully!"