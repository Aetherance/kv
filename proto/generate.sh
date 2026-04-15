#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_DIR="${ROOT_DIR}/proto/proto"

mapfile -t proto_files < <(find "${PROTO_DIR}" -type f -name '*.proto' | sort)

if [ "${#proto_files[@]}" -eq 0 ]; then
	echo "no proto files found in ${PROTO_DIR}"
	exit 0
fi

protoc \
	-I "${PROTO_DIR}" \
	--go_out="${ROOT_DIR}" \
	--go_opt=module=github.com/Aetherance/kv \
	"${proto_files[@]}"
