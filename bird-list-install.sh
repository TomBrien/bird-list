#!/usr/bin/env bash
set -Eeuo pipefail

readonly REPOSITORY_URL="https://github.com/TomBrien/bird-list.git"
readonly INSTALL_DIR="${HOME}/bird-list"
readonly PORT_FILE="${INSTALL_DIR}/.env"
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

if ! "${DOCKER[@]}" compose version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required but unavailable." >&2
  exit 1
fi

if [[ -e "$INSTALL_DIR" && ! -d "$INSTALL_DIR/.git" ]]; then
  echo "$INSTALL_DIR exists but is not a Git repository; refusing to overwrite it." >&2
  exit 1
fi

if [[ -d "$INSTALL_DIR/.git" ]]; then
  git -C "$INSTALL_DIR" pull --ff-only
else
  git clone "$REPOSITORY_URL" "$INSTALL_DIR"
fi

port="$(choose_port)"
printf 'BIRD_LIST_PORT=%s\n' "$port" >"$PORT_FILE"

cd "$INSTALL_DIR"
"${DOCKER[@]}" compose up --build -d

echo "bird-list is running at http://localhost:${port}"
echo "Docker will restart the container automatically after system startup."
