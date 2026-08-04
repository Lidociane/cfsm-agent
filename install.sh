#!/bin/sh
set -eu

REPO="${CF_PROBE_REPO:-huilang-me/cfsm-agent}"
SERVICE_NAME="${CF_PROBE_SERVICE_NAME:-cf-probe}"
GITHUB_PROXY="${CF_PROBE_GH_PROXY:-}"
INSTALL_VERSION="${CF_PROBE_VERSION:-latest}"

log() {
    printf '%s\n' "$*"
}

die() {
    printf '[ERROR] %s\n' "$*" >&2
    exit 1
}

need_value_for=""
for arg in "$@"; do
    if [ -n "$need_value_for" ]; then
        case "$need_value_for" in
            proxy) GITHUB_PROXY="$arg" ;;
            version) INSTALL_VERSION="$arg" ;;
            service) SERVICE_NAME="$arg" ;;
        esac
        need_value_for=""
        continue
    fi
    case "$arg" in
        --install-ghproxy=*) GITHUB_PROXY="${arg#*=}" ;;
        --install-ghproxy) need_value_for="proxy" ;;
        --install-version=*) INSTALL_VERSION="${arg#*=}" ;;
        --install-version) need_value_for="version" ;;
        --service_name=*|--service-name=*|-service_name=*|-service-name=*) SERVICE_NAME="${arg#*=}" ;;
        --service_name|--service-name|-service_name|-service-name) need_value_for="service" ;;
    esac
done

detect_os() {
    os="$(uname -s 2>/dev/null || printf unknown)"
    case "$os" in
        Linux) printf linux ;;
        Darwin) printf darwin ;;
        MINGW*|MSYS*|CYGWIN*) printf windows ;;
        *) die "unsupported OS: $os" ;;
    esac
}

detect_arch() {
    arch="$(uname -m 2>/dev/null || printf unknown)"
    case "$arch" in
        x86_64|amd64) printf amd64 ;;
        aarch64|arm64) printf arm64 ;;
        i386|i686) printf 386 ;;
        armv5*) printf armv5 ;;
        armv6*) printf armv6 ;;
        armv7*|armv8l) printf armv7 ;;
        mips) printf mips-softfloat ;;
        mipsel|mipsle) printf mipsle-softfloat ;;
        mips64) printf mips64 ;;
        mips64el|mips64le) printf mips64le ;;
        loongarch64|loong64) printf loong64 ;;
        riscv64) printf riscv64 ;;
        *) die "unsupported architecture: $arch" ;;
    esac
}

download() {
    url="$1"
    out="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fL --connect-timeout 10 -m 120 -o "$out" "$url"
    elif command -v wget >/dev/null 2>&1; then
        wget -O "$out" "$url"
    else
        die "curl or wget is required for bootstrap download"
    fi
}

find_installed_binary() {
    home_dir="${HOME:-}"
    for p in "/usr/local/bin/$SERVICE_NAME" "/usr/bin/$SERVICE_NAME" "$home_dir/.cf-probe/bin/$SERVICE_NAME"; do
        if [ -x "$p" ]; then
            printf '%s' "$p"
            return 0
        fi
    done
    return 1
}

cmd="${1:-install}"
case "$cmd" in
    uninstall|remove|delete|purge)
        if bin="$(find_installed_binary)"; then
            exec "$bin" "$@"
        fi
        die "installed $SERVICE_NAME binary was not found"
        ;;
esac

os_name="$(detect_os)"
arch_name="$(detect_arch)"
asset="cf-probe-${os_name}-${arch_name}"
if [ "$os_name" = "windows" ]; then
    asset="${asset}.exe"
fi

if [ "$INSTALL_VERSION" = "latest" ]; then
    path="latest/download"
else
    path="download/$INSTALL_VERSION"
fi
url="https://github.com/$REPO/releases/$path/$asset"
if [ -n "$GITHUB_PROXY" ]; then
    url="${GITHUB_PROXY%/}/$url"
fi

tmp="${TMPDIR:-/tmp}/cf-probe-bootstrap.$$"
trap 'rm -f "$tmp"' EXIT INT TERM

log "CF-Server-Monitor Go Probe bootstrap"
log "  repo    : $REPO"
log "  version : $INSTALL_VERSION"
log "  target  : $os_name/$arch_name"
log "  asset   : $asset"
log "  url     : $url"

download "$url" "$tmp"
chmod +x "$tmp"

if [ "$#" -eq 0 ]; then
    exec "$tmp" install
fi
exec "$tmp" "$@"
