#!/usr/bin/env sh

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

ARCHIVE_PATH=${1:-"$ROOT_DIR/pii-masker-image.tar.gz"}
IMAGE_REF=${IMAGE_REF:-pii-masker:latest}
CONTAINER_NAME=${CONTAINER_NAME:-pii-masker}
HOST_PORT=${HOST_PORT:-18080}
CONTAINER_PORT=${CONTAINER_PORT:-8080}
DATA_DIR=${DATA_DIR:-}
VOLUME_NAME=${VOLUME_NAME:-"$CONTAINER_NAME-data"}
PUBLIC_BASE_URL=${PII_MASKER_PUBLIC_BASE_URL:-"http://localhost:$HOST_PORT"}
FORCE_RECREATE=${FORCE_RECREATE:-0}

if ! command -v docker >/dev/null 2>&1; then
  echo "docker command not found" >&2
  exit 1
fi

if [ ! -f "$ARCHIVE_PATH" ]; then
  echo "image archive not found: $ARCHIVE_PATH" >&2
  exit 1
fi

echo "Loading image archive: $ARCHIVE_PATH"
LOAD_OUTPUT=$(docker load -i "$ARCHIVE_PATH")
printf '%s\n' "$LOAD_OUTPUT"

LOADED_IMAGE=$(printf '%s\n' "$LOAD_OUTPUT" | awk -F': ' '/^Loaded image:/ {print $2; exit}')
IMAGE_TO_RUN=${LOADED_IMAGE:-$IMAGE_REF}

if ! docker image inspect "$IMAGE_TO_RUN" >/dev/null 2>&1; then
  echo "loaded image not found locally: $IMAGE_TO_RUN" >&2
  exit 1
fi

if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  if [ "$FORCE_RECREATE" = "1" ]; then
    echo "Removing existing container: $CONTAINER_NAME"
    docker rm -f "$CONTAINER_NAME" >/dev/null
  else
    echo "container already exists: $CONTAINER_NAME" >&2
    echo "set FORCE_RECREATE=1 to remove and recreate it" >&2
    exit 1
  fi
fi

MOUNT_DESCRIPTION=
set -- docker run -d \
  --name "$CONTAINER_NAME" \
  -p "$HOST_PORT:$CONTAINER_PORT"

if [ -n "$DATA_DIR" ]; then
  mkdir -p "$DATA_DIR" "$DATA_DIR/jobs" "$DATA_DIR/logs"
  chmod 0777 "$DATA_DIR" "$DATA_DIR/jobs" "$DATA_DIR/logs" 2>/dev/null || true
  set -- "$@" -v "$DATA_DIR:/app/data"
  MOUNT_DESCRIPTION=$DATA_DIR
else
  set -- "$@" -v "$VOLUME_NAME:/app/data"
  MOUNT_DESCRIPTION=$VOLUME_NAME
fi

echo "Starting container: $CONTAINER_NAME"
set -- "$@" \
  -e PII_MASKER_ADDR=":$CONTAINER_PORT" \
  -e PII_MASKER_PUBLIC_BASE_URL="$PUBLIC_BASE_URL" \
  -e PII_MASKER_STORAGE_DIR=/app/data \
  -e PII_MASKER_ENABLE_EMBEDDED_UPSTAGE_MOCK=true \
  -e PII_MASKER_UPSTAGE_BASE_URL="http://127.0.0.1:$CONTAINER_PORT/internal/mock/upstage/inference" \
  -e PII_MASKER_DEFAULT_MODEL=pii \
  -e PII_MASKER_DEFAULT_LANG=ko \
  -e PII_MASKER_DEFAULT_SCHEMA=oac \
  -e PII_MASKER_ENABLE_DEBUG=true \
  "$IMAGE_TO_RUN"

"$@"

printf '\n'
echo "PII Masker is starting."
echo "UI:     $PUBLIC_BASE_URL/"
echo "Health: $PUBLIC_BASE_URL/v1/health"
echo "Data:   $MOUNT_DESCRIPTION"
