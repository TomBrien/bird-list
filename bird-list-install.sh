#!/usr/bin/env bash
set -Eeuo pipefail

readonly REPOSITORY_URL="https://github.com/TomBrien/bird-list.git"
readonly INSTALL_DIR="${HOME}/bird-list"
readonly PORT_FILE="${INSTALL_DIR}/.env"
readonly DATA_BACKUP_DIR="${HOME}/.local/share/bird-list"
if ((EUID == 0)); then
  SUDO=()
elif command -v sudo >/dev/null 2>&1; then
  SUDO=(sudo)
else
  echo "This installer needs root access. Install sudo or run it as root." >&2
  exit 1
fi

require_command() {
  command -v "$1" >/dev/null 2>&1
}

install_packages() {
  if require_command apt-get; then
    "${SUDO[@]}" apt-get update
    "${SUDO[@]}" apt-get install -y "$@"
  elif require_command dnf; then
    "${SUDO[@]}" dnf install -y "$@"
  elif require_command pacman; then
    "${SUDO[@]}" pacman -Sy --noconfirm "$@"
  else
    echo "Unsupported package manager. Install these packages manually: $*" >&2
    exit 1
  fi
}

install_docker() {
  if ! require_command docker; then
    echo "Installing Docker..."
    if require_command apt-get; then
      install_packages docker.io docker-compose-plugin
    elif require_command dnf; then
      install_packages docker docker-compose-plugin
    elif require_command pacman; then
      install_packages docker docker-compose
    fi
  fi

  if ! require_command systemctl; then
    echo "systemd is required to start Docker automatically at boot." >&2
    exit 1
  fi
  "${SUDO[@]}" systemctl enable --now docker
  if ((EUID != 0)); then
    "${SUDO[@]}" usermod -aG docker "$USER"
  fi
}

arm64_plugin_arch() {
  case "$(uname -m)" in
    aarch64|arm64)
      printf '%s\n' aarch64
      ;;
    *)
      echo "This installer supports Linux arm64 hosts only." >&2
      exit 1
      ;;
  esac
}

version_is_at_least() {
  local version="$1"
  local minimum="$2"
  [[ "$(printf '%s\n%s\n' "$minimum" "$version" | sort -V | head -n1)" == "$minimum" ]]
}

install_compose() {
  if "${DOCKER[@]}" compose version >/dev/null 2>&1; then
    return
  fi

  local compose_arch
  compose_arch="$(arm64_plugin_arch)"

  if ! require_command curl; then
    echo "Installing curl to download Docker Compose..."
    install_packages curl ca-certificates
  fi

  local compose_plugin
  compose_plugin="$(mktemp)"
  trap 'rm -f "$compose_plugin"' RETURN
  curl -fsSL "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-${compose_arch}" -o "$compose_plugin"
  "${SUDO[@]}" install -D -m 0755 "$compose_plugin" /usr/local/lib/docker/cli-plugins/docker-compose

  if ! "${DOCKER[@]}" compose version >/dev/null 2>&1; then
    echo "Docker Compose v2 installation failed." >&2
    exit 1
  fi
}

install_buildx() {
  local buildx_version
  buildx_version="$("${DOCKER[@]}" buildx version 2>/dev/null | sed -n 's/.* v\{0,1\}\([0-9][0-9.]*\).*/\1/p')"
  if [[ -n "$buildx_version" ]] && version_is_at_least "$buildx_version" "0.17.0"; then
    return
  fi

  echo "Installing Docker Buildx 0.17.1..."
  local buildx_arch
  buildx_arch="$(arm64_plugin_arch)"
  local buildx_plugin
  buildx_plugin="$(mktemp)"
  trap 'rm -f "$buildx_plugin"' RETURN
  curl -fsSL "https://github.com/docker/buildx/releases/download/v0.17.1/buildx-v0.17.1.linux-${buildx_arch}" -o "$buildx_plugin"
  "${SUDO[@]}" install -D -m 0755 "$buildx_plugin" /usr/local/lib/docker/cli-plugins/docker-buildx

  buildx_version="$("${DOCKER[@]}" buildx version 2>/dev/null | sed -n 's/.* v\{0,1\}\([0-9][0-9.]*\).*/\1/p')"
  if [[ -z "$buildx_version" ]] || ! version_is_at_least "$buildx_version" "0.17.0"; then
    echo "Docker Buildx 0.17.0 or later installation failed." >&2
    exit 1
  fi
}

port_is_in_use() {
  local port="$1"
  if require_command ss; then
    ss -H -ltn "sport = :${port}" | grep -q .
  elif require_command netstat; then
    netstat -ltn | awk '{print $4}' | grep -Eq "[:.]${port}$"
  else
    echo "Neither ss nor netstat is available to check port availability." >&2
    exit 1
  fi
}

choose_port() {
  local port=8080
  while port_is_in_use "$port"; do
    ((port++))
    if ((port > 65535)); then
      echo "No available non-privileged TCP port was found." >&2
      exit 1
    fi
  done
  printf '%s\n' "$port"
}

if ! require_command git; then
  echo "Installing Git..."
  install_packages git
fi

install_docker

if docker info >/dev/null 2>&1; then
  DOCKER=(docker)
elif "${SUDO[@]}" docker info >/dev/null 2>&1; then
  DOCKER=("${SUDO[@]}" docker)
else
  echo "Docker is installed but its daemon is unavailable." >&2
  exit 1
fi

install_compose
install_buildx

if [[ -e "$INSTALL_DIR" && ! -d "$INSTALL_DIR/.git" ]]; then
  echo "$INSTALL_DIR exists but is not a Git repository; refusing to overwrite it." >&2
  exit 1
fi

if [[ -d "$INSTALL_DIR/.git" ]]; then
  git -C "$INSTALL_DIR" pull --ff-only
else
  git clone "$REPOSITORY_URL" "$INSTALL_DIR"
fi

if [[ -d "$DATA_BACKUP_DIR" && ! -e "$INSTALL_DIR/data" ]]; then
  mv "$DATA_BACKUP_DIR" "$INSTALL_DIR/data"
fi

port="$(choose_port)"
printf 'BIRD_LIST_PORT=%s\n' "$port" >"$PORT_FILE"

cd "$INSTALL_DIR"
"${DOCKER[@]}" compose up --build -d

echo "bird-list is running at http://localhost:${port}"
echo "Docker will restart the container automatically after system startup."
