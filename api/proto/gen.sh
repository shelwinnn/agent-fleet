#!/usr/bin/env bash
# gen.sh — 再生成 api/proto/fleet/v1 下的 Go 代码（架构 §8.2 契约的来源是本目录 .proto）。
#
# 依赖：
#   - protoc（≥3.x）
#   - protoc-gen-go    ：go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   - protoc-gen-go-grpc：go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
#
# 说明：本片交付环境为离线环境，仅有 protoc 与 protoc-gen-go；agent_grpc.pb.go 按
# protoc-gen-go-grpc v1.5.1 的输出格式手写维护（文件头有 PROVENANCE 标注）。在插件
# 可用的环境执行本脚本会经 --go-grpc_out 再生成并覆盖该文件，以插件输出为准。
set -euo pipefail

cd "$(dirname "$0")/.." # api/proto

PROTO_FILES=(fleet/v1/*.proto)

if ! command -v protoc >/dev/null; then
  echo "error: protoc not found" >&2
  exit 1
fi

out_opts="paths=source_relative"
if command -v protoc-gen-go >/dev/null; then
  protoc "--go_out=${out_opts}":. "--go_opt=${out_opts}" "${PROTO_FILES[@]}"
  echo "generated: fleet/v1/*.pb.go (protoc-gen-go)"
else
  echo "warning: protoc-gen-go not found; *.pb.go left unchanged" >&2
fi

if command -v protoc-gen-go-grpc >/dev/null; then
  protoc "--go-grpc_out=${out_opts}":. "--go-grpc_opt=${out_opts}" fleet/v1/agent.proto
  echo "generated: fleet/v1/agent_grpc.pb.go (protoc-gen-go-grpc)"
else
  echo "warning: protoc-gen-go-grpc not found; agent_grpc.pb.go (hand-maintained, v1.5.1 format) left unchanged" >&2
fi
