#!/usr/bin/env bash
#
# 本地测试脚本：模拟异步拉取镜像并轮询状态
#
# 用法：
#   ./scripts/test_async_pull.sh [IMAGE]
#
# 环境变量：
#   IMAGE      要拉取的镜像（默认 pytorch/pytorch:2.4.0-cuda12.1-cudnn9-runtime，较大镜像可观察到进度）
#   MCP_URL    MCP HTTP 服务地址（默认 http://localhost:18080/mcp）
#   POLL_INTERVAL  轮询间隔秒数（默认 3）
#   MCP_API_KEY    鉴权 Key（若服务开启了鉴权）
#
# 示例：
#   ./scripts/test_async_pull.sh                          # 默认大镜像
#   IMAGE=nginx:latest ./scripts/test_async_pull.sh       # 小镜像快速验证
#   ./scripts/test_async_pull.sh myregistry.com/app:2.0   # 指定镜像

set -euo pipefail

IMAGE="${1:-${IMAGE:-pytorch/pytorch:2.4.0-cuda12.1-cudnn9-runtime}}"
MCP_URL="${MCP_URL:-http://localhost:18080/mcp}"
POLL_INTERVAL="${POLL_INTERVAL:-3}"
AUTH_ARGS=()
[ -n "${MCP_API_KEY:-}" ] && AUTH_ARGS=(-H "Authorization: Bearer ${MCP_API_KEY}")

call_tool() { # $1=tool_name  $2=arguments_json
  curl -s -X POST "${MCP_URL}" ${AUTH_ARGS[@]+"${AUTH_ARGS[@]}"} \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"id\":$RANDOM,\"params\":{\"name\":\"$1\",\"arguments\":$2}}"
}

parse_text() { # 从 JSON-RPC 响应中提取 content[0].text
  python3 -c '
import json, sys
d = json.load(sys.stdin)
err = d.get("error")
if err:
    print(f"RPC error: {err}", file=sys.stderr)
    sys.exit(1)
print(d["result"]["content"][0]["text"])
'
}

echo "==> 测试开始"
echo "    镜像: ${IMAGE}"
echo "    服务: ${MCP_URL}"
echo ""

# 1. 异步发起拉取
echo "==> [1/3] 调用 pullImage(detach=true) 异步拉取..."
RESP=$(call_tool pullImage "{\"image\":\"${IMAGE}\",\"detach\":true}")
TEXT=$(echo "${RESP}" | parse_text)
echo "${TEXT}"

TASK_ID=$(echo "${TEXT}" | sed -n 's/^Task ID: \(.*\)$/\1/p')
if [ -z "${TASK_ID}" ]; then
  echo "!! 未获取到 task_id，响应内容：" >&2
  echo "${RESP}" >&2
  exit 1
fi
echo "    task_id = ${TASK_ID}"
echo ""

# 2. 轮询状态直到结束
echo "==> [2/3] 轮询 imageTaskStatus（间隔 ${POLL_INTERVAL}s）..."
ENDED=""
while [ -z "${ENDED}" ]; do
  sleep "${POLL_INTERVAL}"
  STATUS_TEXT=$(call_tool imageTaskStatus "{\"task_id\":\"${TASK_ID}\"}" | parse_text)
  # 打印当前进度行的最后几行
  echo "${STATUS_TEXT}" | tail -5
  echo "    ----"
  if echo "${STATUS_TEXT}" | grep -qE "^Status: (completed|failed)$"; then
    ENDED="yes"
  fi
done

# 3. 输出最终结果
echo ""
echo "==> [3/3] 最终结果："
echo "${STATUS_TEXT}" | grep -E "^(Status|Error):" || true
if echo "${STATUS_TEXT}" | grep -q "^Status: completed"; then
  echo "    拉取成功 ✔"
  exit 0
else
  echo "    拉取失败 ✘"
  exit 1
fi
