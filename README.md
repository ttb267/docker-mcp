# Docker MCP Server

Docker management tool based on Model Context Protocol (MCP), enabling AI agents to manage Docker containers and services through the MCP protocol.

## 功能特性

| 工具 | 说明 |
|------|------|
| `createContainer` | 创建并启动 Docker 容器 |
| `listContainers` | 获取所有容器列表 |
| `getContainerLogs` | 获取容器日志 |
| `inspectContainer` | 获取容器状态详情 |
| `createComposeService` | 通过 docker-compose 启动服务 |
| `execContainer` | 在运行中的容器内执行命令 |

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

- Go 1.21+
- [docker/docker](https://github.com/docker/docker) - Docker API Go 客户端
- [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) - MCP Go SDK

## 许可证

MIT License