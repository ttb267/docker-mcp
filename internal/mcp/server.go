package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker-mcp/docker-mcp/internal/docker"
	"github.com/docker-mcp/docker-mcp/pkg/compose"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// API Key for Authorization header authentication
var (
	apiKey     string
	apiKeyOnce sync.Once
	apiKeySet  bool
)

// SetAPIKey sets the API key for authentication
func SetAPIKey(key string) {
	apiKeyOnce.Do(func() {
		apiKey = key
		apiKeySet = true
	})
}

// GetAPIKey returns the current API key
func GetAPIKey() string {
	return apiKey
}

// IsAuthEnabled returns whether authentication is enabled
func IsAuthEnabled() bool {
	return apiKeySet && apiKey != ""
}

type Server struct {
	dockerClient *docker.DockerClient
	composeSvc   *compose.ComposeService
	mcpServer    *server.MCPServer
}

func NewServer() (*Server, error) {
	dockerClient, err := docker.NewDockerClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	composeSvc := compose.NewComposeService()

	s := &Server{
		dockerClient: dockerClient,
		composeSvc:   composeSvc,
	}

	s.mcpServer = server.NewMCPServer(
		"docker-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	s.registerTools()
	return s, nil
}

func (s *Server) registerTools() {
	s.mcpServer.AddTool(
		mcp.NewTool("createContainer",
			mcp.WithDescription("Create and start a new Docker container"),
			mcp.WithString("image",
				mcp.Required(),
				mcp.Description("Docker image to use for the container (e.g., nginx:latest)"),
			),
			mcp.WithString("name",
				mcp.Description("Name for the container"),
			),
			mcp.WithString("ports",
				mcp.Description("Port mappings in format host:container (e.g., 8080:80)"),
			),
			mcp.WithString("env",
				mcp.Description("Environment variables (e.g., KEY=VALUE,KEY2=VALUE2)"),
			),
			mcp.WithString("cmd",
				mcp.Description("Command to run in the container (e.g., echo hello)"),
			),
		),
		s.handleCreateContainer,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("listContainers",
			mcp.WithDescription("List all Docker containers"),
		),
		s.handleListContainers,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("listImages",
			mcp.WithDescription("List all Docker images"),
		),
		s.handleListImages,
	)

	// Pull Image tool
	s.mcpServer.AddTool(
		mcp.NewTool("pullImage",
			mcp.WithDescription("Pull an image from registry"),
			mcp.WithString("image",
				mcp.Required(),
				mcp.Description("Image name to pull (e.g., nginx:latest, myregistry.com/myimage:tag)"),
			),
		),
		s.handlePullImage,
	)

	// Tag Image tool
	s.mcpServer.AddTool(
		mcp.NewTool("tagImage",
			mcp.WithDescription("Tag an image with a new name"),
			mcp.WithString("source",
				mcp.Required(),
				mcp.Description("Source image name or ID"),
			),
			mcp.WithString("target",
				mcp.Required(),
				mcp.Description("Target image name and tag"),
			),
		),
		s.handleTagImage,
	)

	// Push Image tool
	s.mcpServer.AddTool(
		mcp.NewTool("pushImage",
			mcp.WithDescription("Push an image to registry"),
			mcp.WithString("image",
				mcp.Required(),
				mcp.Description("Image name to push (e.g., myregistry.com/myimage:tag)"),
			),
		),
		s.handlePushImage,
	)

	// Login to Registry tool
	s.mcpServer.AddTool(
		mcp.NewTool("loginToRegistry",
			mcp.WithDescription("Login to a Docker registry"),
			mcp.WithString("registry",
				mcp.Required(),
				mcp.Description("Registry address (e.g., docker.io, myregistry.com)"),
			),
			mcp.WithString("username",
				mcp.Required(),
				mcp.Description("Username"),
			),
			mcp.WithString("password",
				mcp.Required(),
				mcp.Description("Password"),
			),
		),
		s.handleLoginToRegistry,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("getContainerLogs",
			mcp.WithDescription("Get logs from a specific container"),
			mcp.WithString("container_id",
				mcp.Required(),
				mcp.Description("Container ID or name"),
			),
			mcp.WithString("tail",
				mcp.Description("Number of lines to show from the end of the logs (default: 100)"),
			),
		),
		s.handleGetContainerLogs,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("inspectContainer",
			mcp.WithDescription("Get detailed information about a container"),
			mcp.WithString("container_id",
				mcp.Required(),
				mcp.Description("Container ID or name"),
			),
		),
		s.handleInspectContainer,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("createComposeService",
			mcp.WithDescription("Start services using docker-compose"),
			mcp.WithString("compose_file",
				mcp.Required(),
				mcp.Description("Path to docker-compose.yml file"),
			),
			mcp.WithString("project_name",
				mcp.Description("Project name for docker-compose"),
			),
		),
		s.handleCreateComposeService,
	)

	s.mcpServer.AddTool(
		mcp.NewTool("execContainer",
			mcp.WithDescription("Execute a command in a running container. Long-running commands (modelscope, wget, curl, download, etc.) will auto-stream output."),
			mcp.WithString("container_id",
				mcp.Required(),
				mcp.Description("Container ID or name"),
			),
			mcp.WithString("cmd",
				mcp.Required(),
				mcp.Description("Command to execute"),
			),
			mcp.WithString("env",
				mcp.Description("Environment variables (e.g., HTTP_PROXY=http://proxy:8080)"),
			),
		),
		s.handleExecContainer,
	)

	// execContainerStatus tool for checking detached command status
	s.mcpServer.AddTool(
		mcp.NewTool("execContainerStatus",
			mcp.WithDescription("Check the status of a detached exec command"),
			mcp.WithString("exec_id",
				mcp.Required(),
				mcp.Description("Exec ID returned from execContainer with detach=true"),
			),
		),
		s.handleExecContainerStatus,
	)
}

func (s *Server) handleCreateContainer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleCreateContainer called")
	image := request.GetString("image", "")
	if image == "" {
		return mcp.NewToolResultError("image is required"), nil
	}

	name := request.GetString("name", "")
	portsStr := request.GetString("ports", "")
	envStr := request.GetString("env", "")
	cmdStr := request.GetString("cmd", "")

	// Security check: validate cmd if provided
	if cmdStr != "" {
		// Join the cmd parts to check as a single string
		cmdCheck := strings.Join(splitAndTrim(cmdStr), " ")
		if allowed, reason := isContainerCmdAllowed(cmdCheck); !allowed {
			return mcp.NewToolResultError(fmt.Sprintf("Security rejected: %s", reason)), nil
		}
	}

	var ports []string
	if portsStr != "" {
		ports = splitAndTrim(portsStr)
	}

	var env []string
	if envStr != "" {
		env = splitAndTrim(envStr)
	}

	var cmd []string
	if cmdStr != "" {
		cmd = strings.Fields(cmdStr) // Split by whitespace, not comma
	}

	containerID, err := s.dockerClient.CreateContainer(ctx, docker.ContainerConfig{
		Image: image,
		Name:  name,
		Ports: ports,
		Env:   env,
		Cmd:   cmd,
	})

	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to create container: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Container created successfully: %s", containerID)), nil
}

func (s *Server) handleListContainers(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleListContainers called")
	containers, err := s.dockerClient.ListContainers(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to list containers: %v", err)), nil
	}

	if len(containers) == 0 {
		return mcp.NewToolResultText("No containers found"), nil
	}

	result := "Containers:\n"
	for _, c := range containers {
		result += fmt.Sprintf("- ID: %s, Name: %v, Image: %s, Status: %s, State: %s\n",
			c.ID[:12], c.Names, c.Image, c.Status, c.State)
	}

	return mcp.NewToolResultText(result), nil
}

func (s *Server) handleListImages(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleListImages called")
	images, err := s.dockerClient.ListImages(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to list images: %v", err)), nil
	}

	if len(images) == 0 {
		return mcp.NewToolResultText("No images found"), nil
	}

	result := "Images:\n"
	for _, img := range images {
		tags := "<none>"
		if len(img.RepoTags) > 0 {
			tags = fmt.Sprintf("%v", img.RepoTags)
		}
		sizeMB := float64(img.Size) / 1024 / 1024
		result += fmt.Sprintf("- ID: %s, Tags: %s, Size: %.2f MB\n",
			img.ID[:12], tags, sizeMB)
	}

	return mcp.NewToolResultText(result), nil
}

func (s *Server) handlePullImage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handlePullImage called")
	image := request.GetString("image", "")
	if image == "" {
		return mcp.NewToolResultError("image is required"), nil
	}

	log.Printf("[INFO] Pulling image: %s", image)
	err := s.dockerClient.PullImage(ctx, image)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to pull image: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Image pulled successfully: %s", image)), nil
}

func (s *Server) handleTagImage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleTagImage called")
	source := request.GetString("source", "")
	target := request.GetString("target", "")
	if source == "" || target == "" {
		return mcp.NewToolResultError("source and target are required"), nil
	}

	err := s.dockerClient.TagImage(ctx, source, target)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to tag image: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Image tagged: %s -> %s", source, target)), nil
}

func (s *Server) handlePushImage(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handlePushImage called")
	image := request.GetString("image", "")
	if image == "" {
		return mcp.NewToolResultError("image is required"), nil
	}

	log.Printf("[INFO] Pushing image: %s", image)
	err := s.dockerClient.PushImage(ctx, image)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to push image: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Image pushed successfully: %s", image)), nil
}

func (s *Server) handleLoginToRegistry(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleLoginToRegistry called")
	registry := request.GetString("registry", "")
	username := request.GetString("username", "")
	password := request.GetString("password", "")

	if registry == "" || username == "" || password == "" {
		return mcp.NewToolResultError("registry, username, password are required"), nil
	}

	log.Printf("[INFO] Logging in to registry: %s", registry)
	err := s.dockerClient.LoginToRegistry(ctx, registry, username, password)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to login: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Login successful to %s", registry)), nil
}

func (s *Server) handleGetContainerLogs(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleGetContainerLogs called")
	containerID := request.GetString("container_id", "")
	if containerID == "" {
		return mcp.NewToolResultError("container_id is required"), nil
	}

	tail := request.GetString("tail", "100")

	logs, err := s.dockerClient.GetContainerLogs(ctx, containerID, tail)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get container logs: %v", err)), nil
	}

	if logs == "" {
		return mcp.NewToolResultText("No logs available"), nil
	}

	return mcp.NewToolResultText(logs), nil
}

func (s *Server) handleInspectContainer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleInspectContainer called")
	containerID := request.GetString("container_id", "")
	if containerID == "" {
		return mcp.NewToolResultError("container_id is required"), nil
	}

	info, err := s.dockerClient.InspectContainer(ctx, containerID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to inspect container: %v", err)), nil
	}

	state := "unknown"
	if info.State != nil {
		state = info.State.Status
	}

	result := fmt.Sprintf(`Container: %s
Name: %s
Image: %s
Status: %s
State: %s
Created: %s
`,
		info.ID[:12],
		info.Name,
		info.Config.Image,
		state,
		state,
		info.Created,
	)

	return mcp.NewToolResultText(result), nil
}

func (s *Server) handleCreateComposeService(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleCreateComposeService called")
	composeFile := request.GetString("compose_file", "")
	if composeFile == "" {
		return mcp.NewToolResultError("compose_file is required"), nil
	}

	projectName := request.GetString("project_name", "")

	result, err := s.composeSvc.Up(ctx, composeFile, projectName)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to start compose services: %v", err)), nil
	}

	return mcp.NewToolResultText(result), nil
}

// allowedCommands defines the allowed command patterns for execContainer
var allowedCommands = []string{
	"modelscope",
	"docker pull",
	"docker tag",
	"docker login",
	"docker push",
	"ls",
	"ll",
	"dir",
	"pwd",
	"whoami",
	"wget",
	"curl",
	"tar",
	"unzip",
	"gunzip",
	"bunzip2",
	"xz",
	"unxz",
}

// allowedContainerCommands defines allowed commands for container startup (createContainer)
var allowedContainerCommands = []string{
	"sleep",
	"tail",
	"cat",
	"echo",
	"ping",
	"true",
	"false",
	"date",
	"hostname",
	"id",
	"uname",
	"top",
	"htop",
}

// dangerousCommands defines commands that are not allowed
var dangerousCommands = []string{
	"rm",
	"mv",
	"cp",
	"echo",
	">",
	">>",
	"chmod",
	"chown",
	"touch",
	"mkdir",
	"rmdir",
	"unlink",
	"ln",
	"sed",
	"awk",
	"perl",
	"python",
	"python3",
	"node",
	"bash",
	"sh",
	"powershell",
	"nc",
	"netcat",
	"ssh",
	"scp",
	"ftp",
	"sftp",
}

// isContainerCmdAllowed checks if command is allowed for container startup (createContainer)
func isContainerCmdAllowed(cmdStr string) (bool, string) {
	// First check for dangerous commands
	lowerCmd := strings.ToLower(cmdStr)
	for _, dangerous := range dangerousCommands {
		if strings.Contains(lowerCmd, dangerous+" ") ||
			strings.HasPrefix(lowerCmd, dangerous) ||
			strings.Contains(lowerCmd, " "+dangerous) ||
			strings.Contains(lowerCmd, "|"+dangerous) ||
			strings.Contains(lowerCmd, "&&"+dangerous) ||
			strings.Contains(lowerCmd, "; "+dangerous) {
			log.Printf("[SECURITY] [REJECTED] createContainer - Command blocked: '%s' in cmd: '%s' - %s",
				dangerous, cmdStr, time.Now().Format(time.RFC3339))
			return false, fmt.Sprintf("Command '%s' is not allowed for security reasons", dangerous)
		}
	}

	// Check if command matches allowed patterns for container startup
	for _, allowed := range allowedContainerCommands {
		if strings.Contains(lowerCmd, allowed) {
			log.Printf("[SECURITY] [ALLOWED] createContainer - Command allowed: '%s' in cmd: '%s' - %s",
				allowed, cmdStr, time.Now().Format(time.RFC3339))
			return true, fmt.Sprintf("%s command is allowed for container startup", allowed)
		}
	}

	log.Printf("[SECURITY] [REJECTED] createContainer - No allowed command found in cmd: '%s' - %s",
		cmdStr, time.Now().Format(time.RFC3339))
	return false, "Only safe commands like sleep, tail, cat, echo, ping, etc. are allowed for container startup"
}

func isCommandAllowed(cmdStr string) (bool, string) {
	lowerCmd := strings.ToLower(cmdStr)

	// Check for dangerous commands FIRST - reject immediately if found
	// Split by delimiters and check each token to avoid false positives (e.g., "push" contains "sh")
	tokens := regexp.MustCompile(`[\s|&;]+`).Split(cmdStr, -1)
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		for _, dangerous := range dangerousCommands {
			if token == dangerous {
				log.Printf("[SECURITY] [REJECTED] execContainer - Command blocked: '%s' in cmd: '%s' - %s",
					dangerous, cmdStr, time.Now().Format(time.RFC3339))
				return false, fmt.Sprintf("Command '%s' is not allowed for security reasons", dangerous)
			}
		}
	}

	// Only if no dangerous commands found, check if it's in allowed list
	for _, allowed := range allowedCommands {
		if strings.Contains(lowerCmd, allowed) {
			log.Printf("[SECURITY] [ALLOWED] execContainer - Command allowed: '%s' in cmd: '%s' - %s",
				allowed, cmdStr, time.Now().Format(time.RFC3339))
			reason := fmt.Sprintf("%s command is allowed", allowed)
			return true, reason
		}
	}

	// If not in allowed list and not dangerous, reject
	log.Printf("[SECURITY] [REJECTED] execContainer - No allowed command found in cmd: '%s' - %s",
		cmdStr, time.Now().Format(time.RFC3339))
	return false, "Only modelscope download, docker pull, docker tag, docker login, docker push commands are allowed"
}

// isLongRunningCommand checks if command is a long-running task that should auto-detach
func isLongRunningCommand(cmdStr string) bool {
	lowerCmd := strings.ToLower(cmdStr)
	longRunningKeywords := []string{
		"modelscope",
		"download",
		"wget",
		"curl",
		"pip install",
		"pip3 install",
		"apt-get install",
		"apk add",
		"git clone",
		"git pull",
		"docker pull",
		"tar -",
		"unzip",
		"gunzip",
	}

	for _, keyword := range longRunningKeywords {
		if strings.Contains(lowerCmd, keyword) {
			return true
		}
	}
	return false
}

func (s *Server) handleExecContainer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleExecContainer called")
	containerID := request.GetString("container_id", "")
	if containerID == "" {
		return mcp.NewToolResultError("container_id is required"), nil
	}

	cmdStr := request.GetString("cmd", "")
	if cmdStr == "" {
		return mcp.NewToolResultError("cmd is required"), nil
	}

	// Get optional env variables (e.g., "HTTP_PROXY=http://proxy:8080,HTTPS_PROXY=http://proxy:8080")
	envStr := request.GetString("env", "")
	var env []string
	if envStr != "" {
		env = splitAndTrim(envStr)
	}

	// Get optional detach parameter (default: false)
	// Get optional detach parameter (default: false)
	userDetach := request.GetBool("detach", false)

	// Auto-detach for long-running commands
	detach := userDetach || isLongRunningCommand(cmdStr)

	// Security check: validate command is allowed
	if allowed, reason := isCommandAllowed(cmdStr); !allowed {
		return mcp.NewToolResultError(fmt.Sprintf("Security rejected: %s", reason)), nil
	}

	// Split command string into slice (by whitespace, not comma)
	cmd := strings.Fields(cmdStr)

	result, err := s.dockerClient.ExecContainer(ctx, containerID, cmd, env, detach)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to exec in container: %v", err)), nil
	}

	output := fmt.Sprintf("Exit Code: %d\nOutput: %s", result.ExitCode, result.Output)
	return mcp.NewToolResultText(output), nil
}

func (s *Server) handleExecContainerStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleExecContainerStatus called")
	execID := request.GetString("exec_id", "")
	if execID == "" {
		return mcp.NewToolResultError("exec_id is required. Use the Exec ID returned from execContainer with detach=true"), nil
	}

	result, err := s.dockerClient.ExecContainerStatus(ctx, execID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get exec status: %v", err)), nil
	}

	output := fmt.Sprintf("Exec ID: %s\n%s", result.ExecID, result.Output)
	return mcp.NewToolResultText(output), nil
}

func (s *Server) RunStdio() error {
	return server.ServeStdio(s.mcpServer)
}

// JSON-RPC types for HTTP mode
type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *JSONError  `json:"error,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

type JSONError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (s *Server) RunHTTP(port string) error {
	// Health check endpoint (no auth required)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		// Health check endpoint for ELB and load balancers
		log.Printf("[INFO] /health called from %s", r.RemoteAddr)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Detailed health check endpoint
	http.HandleFunc("/health/detailed", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[INFO] /health/detailed called from %s", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Check Docker connectivity
		ctx := context.Background()
		pingErr := s.dockerClient.Ping(ctx)

		response := map[string]interface{}{
			"status":    "healthy",
			"docker":    "ok",
			"timestamp": time.Now().Format(time.RFC3339),
		}

		if pingErr != nil {
			response["status"] = "unhealthy"
			response["docker"] = "error"
			response["error"] = pingErr.Error()
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		jsonBytes, _ := json.Marshal(response)
		w.Write(jsonBytes)
	})

	// MCP endpoint with authentication
	http.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[INFO] /mcp called from %s, method=%s", r.RemoteAddr, r.Method)
		// Check authentication if enabled
		if IsAuthEnabled() {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "Authorization header required", http.StatusUnauthorized)
				return
			}

			// Support "Bearer <api-key>" format
			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token != apiKey {
				http.Error(w, "Invalid API key", http.StatusUnauthorized)
				return
			}
		}

		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"tools": true,
				},
				"serverInfo": map[string]string{
					"name":    "docker-mcp",
					"version": "1.0.0",
				},
			})
			return
		}

		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer r.Body.Close()

			var request JSONRPCRequest
			if err := json.Unmarshal(body, &request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			response := s.handleJSONRPCRequest(request)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
			return
		}

		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	})

	return http.ListenAndServe(":"+port, nil)
}

func (s *Server) handleJSONRPCRequest(request JSONRPCRequest) JSONRPCResponse {
	ctx := context.Background()

	// Handle initialize request
	if request.Method == "initialize" {
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result: map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{},
				},
				"serverInfo": map[string]string{
					"name":    "docker-mcp",
					"version": "1.0.0",
				},
			},
		}
	}

	// Handle tools/list request
	if request.Method == "tools/list" {
		log.Printf("[INFO] tools/list called")
		tools := []map[string]interface{}{
			{
				"name":        "createContainer",
				"description": "Create and start a new Docker container",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"image": map[string]interface{}{"type": "string", "description": "Docker image to use for the container (e.g., nginx:latest)"},
						"name":  map[string]interface{}{"type": "string", "description": "Name for the container"},
						"ports": map[string]interface{}{"type": "string", "description": "Port mappings in format host:container (e.g., 8080:80)"},
						"env":   map[string]interface{}{"type": "string", "description": "Environment variables (e.g., KEY=VALUE,KEY2=VALUE2)"},
						"cmd":   map[string]interface{}{"type": "string", "description": "Command to run in the container"},
					},
					"required": []string{"image"},
				},
			},
			{
				"name":        "listContainers",
				"description": "List all Docker containers",
				"inputSchema": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
			{
				"name":        "listImages",
				"description": "List all Docker images",
				"inputSchema": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
			{
				"name":        "pullImage",
				"description": "Pull an image from registry",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"image": map[string]interface{}{"type": "string", "description": "Image name to pull"},
					},
					"required": []string{"image"},
				},
			},
			{
				"name":        "tagImage",
				"description": "Tag an image with a new name",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"source": map[string]interface{}{"type": "string", "description": "Source image name or ID"},
						"target": map[string]interface{}{"type": "string", "description": "Target image name and tag"},
					},
					"required": []string{"source", "target"},
				},
			},
			{
				"name":        "pushImage",
				"description": "Push an image to registry",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"image": map[string]interface{}{"type": "string", "description": "Image name to push"},
					},
					"required": []string{"image"},
				},
			},
			{
				"name":        "loginToRegistry",
				"description": "Login to a Docker registry",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"registry": map[string]interface{}{"type": "string", "description": "Registry address"},
						"username": map[string]interface{}{"type": "string", "description": "Username"},
						"password": map[string]interface{}{"type": "string", "description": "Password"},
					},
					"required": []string{"registry", "username", "password"},
				},
			},
			{
				"name":        "getContainerLogs",
				"description": "Get logs from a specific container",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
						"tail":         map[string]interface{}{"type": "string", "description": "Number of lines to show from the end of the logs (default: 100)"},
					},
					"required": []string{"container_id"},
				},
			},
			{
				"name":        "inspectContainer",
				"description": "Get detailed information about a container",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
					},
					"required": []string{"container_id"},
				},
			},
			{
				"name":        "createComposeService",
				"description": "Start services using docker-compose",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"compose_file": map[string]interface{}{"type": "string", "description": "Path to docker-compose.yml file"},
						"project_name": map[string]interface{}{"type": "string", "description": "Project name for docker-compose"},
					},
					"required": []string{"compose_file"},
				},
			},
			{
				"name":        "execContainer",
				"description": "Execute a command in a running container. Long-running commands (modelscope, wget, curl, download, etc.) will auto-stream output.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
						"cmd":          map[string]interface{}{"type": "string", "description": "Command to execute"},
						"env":          map[string]interface{}{"type": "string", "description": "Environment variables (e.g., HTTP_PROXY=http://proxy:8080)"},
					},
					"required": []string{"container_id", "cmd"},
				},
			},
			{
				"name":        "execContainerStatus",
				"description": "Check the status of a detached exec command",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"exec_id": map[string]interface{}{"type": "string", "description": "Exec ID returned from execContainer with detach=true"},
					},
					"required": []string{"exec_id"},
				},
			},
		}
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result:  map[string]interface{}{"tools": tools},
		}
	}

	// Handle tools/call request
	if request.Method == "tools/call" {
		log.Printf("[INFO] tools/call called")
		params, ok := request.Params.(map[string]interface{})
		if !ok {
			return JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      request.ID,
				Error:   &JSONError{Code: -32602, Message: "Invalid params"},
			}
		}

		toolName, ok := params["name"].(string)
		if !ok {
			return JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      request.ID,
				Error:   &JSONError{Code: -32602, Message: "Missing tool name"},
			}
		}

		toolArgs, _ := params["arguments"].(map[string]interface{})

		// Create request using the MCP library
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:      toolName,
				Arguments: toolArgs,
			},
		}

		var result *mcp.CallToolResult
		var err error

		switch toolName {
		case "createContainer":
			result, err = s.handleCreateContainer(ctx, req)
		case "listContainers":
			result, err = s.handleListContainers(ctx, req)
		case "listImages":
			result, err = s.handleListImages(ctx, req)
		case "pullImage":
			result, err = s.handlePullImage(ctx, req)
		case "tagImage":
			result, err = s.handleTagImage(ctx, req)
		case "pushImage":
			result, err = s.handlePushImage(ctx, req)
		case "loginToRegistry":
			result, err = s.handleLoginToRegistry(ctx, req)
		case "getContainerLogs":
			result, err = s.handleGetContainerLogs(ctx, req)
		case "inspectContainer":
			result, err = s.handleInspectContainer(ctx, req)
		case "createComposeService":
			result, err = s.handleCreateComposeService(ctx, req)
		case "execContainer":
			result, err = s.handleExecContainer(ctx, req)
		case "execContainerStatus":
			result, err = s.handleExecContainerStatus(ctx, req)
		default:
			return JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      request.ID,
				Error:   &JSONError{Code: -32601, Message: "Method not found"},
			}
		}

		if err != nil {
			return JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      request.ID,
				Error:   &JSONError{Code: -32000, Message: err.Error()},
			}
		}

		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result:  result,
		}
	}

	// Default response
	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      request.ID,
		Result:  nil,
	}
}

func (s *Server) Close() error {
	if s.dockerClient != nil {
		return s.dockerClient.Close()
	}
	return nil
}

func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, part := range splitCSV(s) {
		trimmed := trimSpaces(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func splitCSV(s string) []string {
	var result []string
	var current []rune
	inQuote := false

	for _, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
		case ',', ';':
			if !inQuote {
				result = append(result, string(current))
				current = nil
				continue
			}
		}
		current = append(current, r)
	}
	if len(current) > 0 {
		result = append(result, string(current))
	}

	return result
}

func trimSpaces(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
