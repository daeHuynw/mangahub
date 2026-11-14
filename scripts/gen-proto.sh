#!/usr/bin/env bash
# Regenerate gRPC stubs from proto/catalog.proto into gen/catalogpb.
# Needs: protoc, protoc-gen-go, protoc-gen-go-grpc (in $(go env GOPATH)/bin).
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PATH:$(go env GOPATH)/bin"
mkdir -p gen/catalogpb
protoc -I proto \
  --go_out=gen/catalogpb --go_opt=paths=source_relative \
  --go-grpc_out=gen/catalogpb --go-grpc_opt=paths=source_relative \
  proto/catalog.proto
echo "generated: $(ls gen/catalogpb)"
