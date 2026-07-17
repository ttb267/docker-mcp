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

### Docker Compose 部署

#### 方式一：无鉴权（开发环境）

```bash
cd deploy
docker-compose up -d
# 访问 http://localhost:8080
```

#### 方式二：Nginx Basic Auth 鉴权（生产环境推荐）

```bash
cd deploy

# 1. 安装 htpasswd 工具 (如果不存在)
# Ubuntu/Debian:
sudo apt install apache2-utils
# CentOS/RHEL:
sudo yum install httpd-tools

# 2. 生成 htpasswd 文件
htpasswd -bc htpasswd admin yourpassword

# 3. 启动服务（需要先创建网络）
docker network create docker-mcp-network
docker-compose -f docker-compose.yaml -f docker-compose.auth.yaml up -d

# 4. 访问 http://localhost:8090
#    输入用户名: admin
#    输入密码: yourpassword
```

**htpasswd 其他用法：**
```bash
# 追加新用户
htpasswd -b htpasswd username password

# 验证用户
htpasswd -v htpasswd username
```

### Kubernetes 部署

#### 方式一：基础部署（无鉴权）

```bash
kubectl apply -f deploy/k8s.yaml
```

#### 方式二：Ingress + Basic Auth 鉴权

```bash
# 1. 生成 htpasswd 文件
htpasswd -bc htpasswd admin yourpassword

# 2. 创建 Secret
kubectl create secret generic docker-mcp-basic-auth \
  --from-file=auth=./htpasswd \
  -n docker-mcp

# 3. 取消 k8s.yaml 中 Ingress 鉴权部分的注释并应用
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

有鉴权（需要配置 API Key）：
```json
{
  "mcpServers": {
    "docker-mcp": {
      "url": "http://localhost:8090/mcp"
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
│   ├── docker-compose.yaml  # Docker Compose 部署（无鉴权）
│   ├── docker-compose.auth.yaml  # 鉴权配置
│   ├── nginx.conf           # Nginx 配置
│   ├── htpasswd             # 鉴权文件（需手动生成）
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