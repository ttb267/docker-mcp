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

### Docker 部署

```bash
# 使用 Docker Compose
cd deploy
docker-compose up -d
```

### Kubernetes 部署

```bash
kubectl apply -f deploy/k8s.yaml
```

## 配置说明

### 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `DOCKER_HOST` | Docker 守护进程地址 | `unix:///var/run/docker.sock` |

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

### HTTP 模式访问

```
# 获取服务能力
GET http://localhost:8080/mcp

# 调用工具
POST http://localhost:8080/mcp
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