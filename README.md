# Docker MCP Server

Docker management tool based on Model Context Protocol (MCP), enabling AI agents to manage Docker containers and services through the MCP protocol.

## 功能特性

| 工具 | 说明 |
|------|------|
| `createContainer` | 创建并启动 Docker 容器（带安全限制） |
| `listContainers` | 获取所有容器列表 |
| `listImages` | 获取所有镜像列表 |
| `pullImage` | 拉取镜像（`detach=true` 可异步后台拉取） |
| `tagImage` | 给镜像打新标签 |
| `pushImage` | 推送镜像（`detach=true` 可异步后台推送） |
| `loginToRegistry` | 登录镜像仓库 |
| `getContainerLogs` | 获取容器日志 |
| `inspectContainer` | 获取容器状态详情 |
| `createComposeService` | 通过 docker-compose 启动服务 |
| `execContainer` | 在运行中的容器内执行命令（带安全限制，长任务自动 detach） |
| `execContainerStatus` | 查询 detach 的 exec 命令状态 |
| `imageTaskStatus` | 轮询异步拉取/推送任务状态与进度 |
| `checkGitHubRelease` | 检查 GitHub 仓库 release / roadmap 更新 |

## 安全机制

### execContainer 命令限制

为保障安全，`execContainer` 工具仅允许以下命令：

**下载/解压命令：**
- `wget`, `curl` - 文件下载
- `tar`, `unzip`, `gunzip`, `bunzip2`, `xz`, `unxz` - 解压

**Docker 操作：**
- `docker pull`, `docker tag`, `docker login`, `docker push`

**AI 模型下载：**
- `modelscope` - ModelScope 模型下载

**查看命令：**
- `ls`, `ll`, `dir`, `pwd`, `whoami`

**危险命令（被阻止）：**
- 文件操作：`rm`, `mv`, `cp`, `echo`, `chmod`, `chown`, `touch`, `mkdir` 等
- Shell：`bash`, `sh`, `powershell`, `python`, `python3`, `node` 等
- 网络：`nc`, `netcat`, `ssh`, `scp`, `ftp` 等

### createContainer 命令限制

容器启动时仅允许以下命令：
- `sleep`, `tail`, `cat`, `echo`, `ping`, `true`, `false`, `date`, `hostname`, `id`, `uname`

### 安全日志

所有安全拦截和允许的操作都会记录到控制台日志：

```
[SECURITY] [REJECTED] execContainer - Command blocked: 'rm' in cmd: 'rm -rf /'
[SECURITY] [ALLOWED] execContainer - Command allowed: 'ls' in cmd: 'ls -la /'
```

## 快速开始

### 本地运行

```bash
# 克隆仓库
git clone https://github.com/ttb267/docker-mcp.git
cd docker-mcp

# 编译
make build

# 运行（STDIO 模式）
./bin/docker-mcp

# 或运行（HTTP 模式）
./bin/docker-mcp --mode http --port 8080
```

## 部署指南

### 构建 Docker 镜像

```bash
# 克隆仓库
git clone https://github.com/ttb267/docker-mcp.git
cd docker-mcp

# 构建本地镜像
make build-image

# 或直接使用 docker
docker build -t docker-mcp:latest .
```

**其他构建选项：**

```bash
# 构建特定平台镜像
make build-imagex86    # x86_64
make build-imagearm64  # ARM64

# 构建并推送到镜像仓库
# 先修改 Makefile 中的 REGISTRY 为你的镜像仓库地址
make push
```

### Docker Compose 部署

```bash
cd deploy
docker-compose up -d

# 访问 http://localhost:8080
```

### Kubernetes 部署

```bash
kubectl apply -f deploy/k8s.yaml
```

## 鉴权配置

### Authorization Header 鉴权（API Key）

MCP Server 支持通过 `Authorization` Header 进行 API Key 鉴权：

```bash
# 方式一：环境变量
export MCP_API_KEY=your-secret-api-key
docker run -d -p 8080:8080 \
  -e MCP_API_KEY=your-secret-api-key \
  -v /var/run/docker.sock:/var/run/docker.sock \
  docker-mcp:latest

# 方式二：命令行参数
docker run -d -p 8080:8080 \
  --api-key=your-secret-api-key \
  -v /var/run/docker.sock:/var/run/docker.sock \
  docker-mcp:latest
```

**客户端配置（Claude Desktop）：**

```json
{
  "mcpServers": {
    "docker-mcp": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer your-secret-api-key"
      }
    }
  }
}
```

### HTTP 请求示例

```bash
# 获取服务能力（需要鉴权）
curl -H "Authorization: Bearer your-secret-api-key" \
  http://localhost:8080/mcp

# 调用工具
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-secret-api-key" \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'
```

## 配置说明

### 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `DOCKER_HOST` | Docker 守护进程地址 | `unix:///var/run/docker.sock` |
| `MCP_API_KEY` | API Key 鉴权密钥 | 无（不启用） |
| `MCP_PORT` | HTTP 服务端口 | `8080` |

### 部署模式

#### 方案一：Socket 挂载（默认）

直接挂载宿主机 Docker Socket：

```yaml
volumes:
  - /var/run/docker.sock:/var/run/docker.sock
environment:
  - DOCKER_HOST=unix:///var/run/docker.sock
```

#### 方案二：TCP 代理

使用 Docker API 代理服务（适用于 K8s 环境）：

```yaml
environment:
  - DOCKER_HOST=tcp://docker-proxy:2375
```

## MCP 客户端配置

### Claude Desktop

无鉴权：
```json
{
  "mcpServers": {
    "docker-mcp": {
      "command": "/path/to/docker-mcp",
      "args": ["--mode", "http", "--port", "8080"]
    }
  }
}
```

有鉴权：
```json
{
  "mcpServers": {
    "docker-mcp": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer your-secret-api-key"
      }
    }
  }
}
```

## 工具使用示例

### 创建容器

```
createContainer(
  image="nginx:latest",
  name="my-nginx",
  ports="8080:80",
  env="KEY=VALUE"
)
```

### 执行命令

```
execContainer(
  container_id="my-container",
  cmd="modelscope download --model Qwen/Qwen2.5-7B"
)
```

### 异步拉取/推送大镜像

大镜像的拉取/推送耗时较长，`pullImage`/`pushImage` 支持 `detach=true` 异步执行：

```bash
# 1. 异步发起拉取，立即返回 task_id（不会阻塞等待）
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "tools/call",
    "id": 1,
    "params": {
      "name": "pullImage",
      "arguments": {"image": "pytorch/pytorch:2.4.0-cuda12.1-cudnn9-runtime", "detach": true}
    }
  }'
# 返回 Task ID: pull-xxx

# 2. 轮询任务状态与进度
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "tools/call",
    "id": 2,
    "params": {
      "name": "imageTaskStatus",
      "arguments": {"task_id": "pull-xxx"}
    }
  }'
```

`imageTaskStatus` 返回示例：

```
Task ID: pull-xxx
Type: pull
Image: busybox:latest
Status: completed
Started: 2026-08-10T11:48:43+08:00
Finished: 2026-08-10T11:48:50+08:00
Recent progress:
  latest: Pulling from library/busybox
  025fe1949698: Downloading 753664/1915158 (39.4%)
  025fe1949698: Pull complete
  Status: Downloaded newer image for busybox:latest
```

> 说明：后台任务有 1 小时超时兜底；任务完成后保留 1 小时自动清理。Agent 提示词写法："用 pullImage 以 detach=true 异步拉取 `xxx:tag`，然后用 imageTaskStatus 轮询直到 completed，failed 则报告错误。"

### 组合示例：检查 GitHub Release 后拉取最新版镜像

结合 `checkGitHubRelease` 检查上游版本、再用 `pullImage` 拉取对应镜像：

```bash
# 1. 检查 sglang 的最新 release（可传 current_version 只返回更新的版本）
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "tools/call",
    "id": 3,
    "params": {
      "name": "checkGitHubRelease",
      "arguments": {
        "repo": "sgl-project/sglang",
        "current_version": "v0.3.0",
        "include_roadmap": true
      }
    }
  }'

# 2. 根据返回的最新版本（如 v0.4.0），异步拉取对应镜像
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "tools/call",
    "id": 4,
    "params": {
      "name": "pullImage",
      "arguments": {"image": "lmsysorg/sglang:latest", "detach": true}
    }
  }'

# 3. 轮询拉取进度
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "tools/call",
    "id": 5,
    "params": {
      "name": "imageTaskStatus",
      "arguments": {"task_id": "pull-xxx"}
    }
  }'
```

对应的 Agent 提示词（一步完成上面三个动作）：

> 用 checkGitHubRelease 检查 `sgl-project/sglang` 的最新版本（我当前是 v0.3.0），把新版本的 release 说明总结给我；然后用 pullImage 以 detach=true 异步拉取对应的 `lmsysorg/sglang:latest` 镜像，并持续用 imageTaskStatus 轮询，直到任务 completed 或 failed。

## 项目结构

```
.
├── cmd/server/main.go       # 入口文件
├── internal/
│   ├── docker/client.go     # Docker 客户端
│   ├── mcp/server.go        # MCP Server
│   └── logging/             # 日志模块
├── pkg/compose/             # Docker Compose 支持
├── deploy/
│   ├── docker-compose.yaml  # Docker Compose 部署
│   └── k8s.yaml            # Kubernetes 部署
├── Dockerfile
└── Makefile
```

## 技术栈

- Go 1.25+
- [docker/docker](https://github.com/docker/docker) - Docker API Go 客户端
- [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) - MCP Go SDK

## 许可证

MIT License