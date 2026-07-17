package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/docker-mcp/docker-mcp/internal/docker"
	"github.com/docker-mcp/docker-mcp/pkg/compose"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

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
			mcp.WithDescription("Execute a command in a running container"),
			mcp.WithString("container_id",
				mcp.Required(),
				mcp.Description("Container ID or name"),
			),
			mcp.WithString("cmd",
				mcp.Required(),
				mcp.Description("Command to execute (e.g., modelscope downloading model)"),
			),
		),
		s.handleExecContainer,
	)
}

func (s *Server) handleCreateContainer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	image := request.GetString("image", "")
	if image == "" {
		return mcp.NewToolResultError("image is required"), nil
	}

	name := request.GetString("name", "")
	portsStr := request.GetString("ports", "")
	envStr := request.GetString("env", "")
	cmdStr := request.GetString("cmd", "")

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
		cmd = splitAndTrim(cmdStr)
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

func (s *Server) handleGetContainerLogs(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

func (s *Server) handleExecContainer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	containerID := request.GetString("container_id", "")
	if containerID == "" {
		return mcp.NewToolResultError("container_id is required"), nil
	}

	cmdStr := request.GetString("cmd", "")
	if cmdStr == "" {
		return mcp.NewToolResultError("cmd is required"), nil
	}

	// Split command string into slice
	cmd := splitAndTrim(cmdStr)

	result, err := s.dockerClient.ExecContainer(ctx, containerID, cmd)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to exec in container: %v", err)), nil
	}

	output := fmt.Sprintf("Exit Code: %d\nOutput: %s", result.ExitCode, result.Output)
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
	// Health check endpoint
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// MCP endpoint
	http.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
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
		tools := []map[string]interface{}{
			{"name": "createContainer", "description": "Create and start a new Docker container"},
			{"name": "listContainers", "description": "List all Docker containers"},
			{"name": "getContainerLogs", "description": "Get logs from a specific container"},
			{"name": "inspectContainer", "description": "Get detailed information about a container"},
			{"name": "createComposeService", "description": "Start services using docker-compose"},
			{"name": "execContainer", "description": "Execute a command in a running container"},
		}
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result:  map[string]interface{}{"tools": tools},
		}
	}

	// Handle tools/call request
	if request.Method == "tools/call" {
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
		case "getContainerLogs":
			result, err = s.handleGetContainerLogs(ctx, req)
		case "inspectContainer":
			result, err = s.handleInspectContainer(ctx, req)
		case "createComposeService":
			result, err = s.handleCreateComposeService(ctx, req)
		case "execContainer":
			result, err = s.handleExecContainer(ctx, req)
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
