#!/usr/bin/env bash
set -Eeuo pipefail

readonly INSTALL_DIR="${HOME}/bird-list"
readonly DATA_BACKUP_DIR="${HOME}/.local/share/bird-list"

if [[ ! -d "$INSTALL_DIR" ]]; then
  echo "bird-list is not installed at $INSTALL_DIR." >&2
  exit 1
fi

if docker info >/dev/null 2>&1; then
  DOCKER=(docker)
elif command -v sudo >/dev/null 2>&1 && sudo docker info >/dev/null 2>&1; then
  DOCKER=(sudo docker)
else
  echo "Docker must be available to stop and remove bird-list." >&2
  exit 1
fi

if ! "${DOCKER[@]}" compose -f "$INSTALL_DIR/compose.yaml" version >/dev/null 2>&1; then
  echo "Docker Compose v2 is required to uninstall bird-list." >&2
  exit 1
fi

read -r -p "Remove all bird-list sighting data? [y/N] " response
case "$response" in
  [yY]|[yY][eE][sS])
    remove_data=true
    ;;
  *)
    remove_data=false
    ;;
esac

"${DOCKER[@]}" compose -f "$INSTALL_DIR/compose.yaml" down --rmi local

if ! "$remove_data" && [[ -d "$INSTALL_DIR/data" ]]; then
  if [[ -e "$DATA_BACKUP_DIR" ]]; then
    echo "Cannot preserve data because $DATA_BACKUP_DIR already exists." >&2
    exit 1
  fi
  mkdir -p "$(dirname "$DATA_BACKUP_DIR")"
  mv "$INSTALL_DIR/data" "$DATA_BACKUP_DIR"
  echo "Sighting data preserved in $DATA_BACKUP_DIR."
fi

cd "$HOME"
rm -rf -- "$INSTALL_DIR"

echo "bird-list has been uninstalled."
