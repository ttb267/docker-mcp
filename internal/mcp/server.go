package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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

	// Background image task management (async pull/push/load)
	taskMu sync.Mutex
	tasks  map[string]*imageTask

	// Background exec task management (async exec commands)
	execTaskMu sync.Mutex
	execTasks  map[string]*execTask
}

// imageTaskStatus represents the state of a background image task
type imageTaskStatus string

const (
	taskRunning   imageTaskStatus = "running"
	taskCompleted imageTaskStatus = "completed"
	taskFailed    imageTaskStatus = "failed"
	taskStopped   imageTaskStatus = "stopped"
)

// imageTask tracks a background pull/push operation
type imageTask struct {
	ID         string
	Type       string // "pull", "push", or "load"
	Image      string
	Status     imageTaskStatus
	Progress   []string // recent progress lines
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
}

// execTask tracks a background exec command (e.g. modelscope download)
type execTask struct {
	ExecID      string
	ContainerID string
	Cmd         string
	Status      imageTaskStatus
	Output      []string // recent output chunks
	Error       string
	StartedAt   time.Time
	FinishedAt  time.Time
}

const (
	// taskTimeout bounds how long a background pull/push may run before being aborted
	taskTimeout = 1 * time.Hour
	// taskTTL is how long finished tasks are kept before cleanup
	taskTTL = 1 * time.Hour
	// taskCleanupInterval is how often finished tasks are swept
	taskCleanupInterval = 10 * time.Minute
)

func NewServer() (*Server, error) {
	dockerClient, err := docker.NewDockerClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	composeSvc := compose.NewComposeService()

	s := &Server{
		dockerClient: dockerClient,
		composeSvc:   composeSvc,
		tasks:        make(map[string]*imageTask),
		execTasks:    make(map[string]*execTask),
	}

	s.mcpServer = server.NewMCPServer(
		"docker-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	s.registerTools()

	// Start background cleanup of finished tasks
	go s.cleanupTasks()

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
			mcp.WithDescription("Pull an image from registry. Set detach=true for large images to run in background and poll with imageTaskStatus. Use platform to pull a specific architecture (e.g., linux/amd64, linux/arm64)."),
			mcp.WithString("image",
				mcp.Required(),
				mcp.Description("Image name to pull (e.g., nginx:latest, myregistry.com/myimage:tag)"),
			),
			mcp.WithString("platform",
				mcp.Description("Target platform for the image (e.g., linux/amd64, linux/arm64). Shorthand: amd64/x86_64/arm64/aarch64 also accepted. Empty = host default."),
			),
			mcp.WithBoolean("detach",
				mcp.Description("Run in background and return a task ID immediately (default: false)"),
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
			mcp.WithDescription("Push an image to registry. Set detach=true for large images to run in background and poll with imageTaskStatus."),
			mcp.WithString("image",
				mcp.Required(),
				mcp.Description("Image name to push (e.g., myregistry.com/myimage:tag)"),
			),
			mcp.WithBoolean("detach",
				mcp.Description("Run in background and return a task ID immediately (default: false)"),
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
			mcp.WithDescription("Execute a command in a running container. Long-running commands (modelscope, wget, curl, download, etc.) will auto-detach and return an exec_id immediately. Use execContainerStatus to check progress and stopExecCommand to stop."),
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
			mcp.WithBoolean("detach",
				mcp.Description("Run in background and return exec_id immediately (default: false). Long-running commands auto-detach."),
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

	// imageTaskStatus tool for polling background pull/push/load task status
	s.mcpServer.AddTool(
		mcp.NewTool("imageTaskStatus",
			mcp.WithDescription("Check the status and progress of a background image task started with pullImage, pushImage, or loadImageFromTar with detach=true"),
			mcp.WithString("task_id",
				mcp.Required(),
				mcp.Description("Task ID returned from pullImage/pushImage/loadImageFromTar with detach=true"),
			),
		),
		s.handleImageTaskStatus,
	)

	// stopExecCommand tool for stopping a running background exec command
	s.mcpServer.AddTool(
		mcp.NewTool("stopExecCommand",
			mcp.WithDescription("Stop a running background exec command (e.g., modelscope download). Useful for interrupting a model download to free bandwidth. modelscope supports resume, so the download can be restarted later."),
			mcp.WithString("exec_id",
				mcp.Required(),
				mcp.Description("Exec ID returned from execContainer with detach=true"),
			),
		),
		s.handleStopExecCommand,
	)

	// loadImageFromTar tool: download tar, docker load, tag, and push
	s.mcpServer.AddTool(
		mcp.NewTool("loadImageFromTar",
			mcp.WithDescription("Download a Docker image tar from a URL, load it with docker load, tag it, and push to a target registry. Supports detach=true for large images."),
			mcp.WithString("tar_url",
				mcp.Required(),
				mcp.Description("URL to download the image tar file from"),
			),
			mcp.WithString("target_image",
				mcp.Required(),
				mcp.Description("Target image name:tag (e.g., myregistry.com/myimage:v1.0)"),
			),
			mcp.WithBoolean("detach",
				mcp.Description("Run in background and return a task ID immediately (default: false)"),
			),
		),
		s.handleLoadImageFromTar,
	)

	// checkGitHubRelease tool for checking GitHub repo releases and roadmap
	s.mcpServer.AddTool(
		mcp.NewTool("checkGitHubRelease",
			mcp.WithDescription("Check a GitHub repository for new releases, release notes, and roadmap updates. Supports proxy via HTTP_PROXY/HTTPS_PROXY env vars and optional GITHUB_TOKEN for higher rate limits."),
			mcp.WithString("repo",
				mcp.Required(),
				mcp.Description("GitHub repository in owner/repo format (e.g., sgl-project/sglang)"),
			),
			mcp.WithString("current_version",
				mcp.Description("Current version you have (e.g., v0.3.0). If provided, only shows newer releases."),
			),
			mcp.WithBoolean("include_roadmap",
				mcp.Description("Whether to fetch roadmap information (default: true)"),
			),
		),
		s.handleCheckGitHubRelease,
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

	platform := normalizePlatform(request.GetString("platform", ""))

	// Async mode: start background pull and return a task ID immediately
	if request.GetBool("detach", false) {
		taskID := s.startImageTask(ctx, "pull", image, func(taskCtx context.Context, task *imageTask) error {
			return s.runPullTask(taskCtx, task, image, platform)
		})
		desc := fmt.Sprintf("Image pull started in background.\nTask ID: %s\nUse imageTaskStatus with this task_id to check progress.", taskID)
		if platform != "" {
			desc = fmt.Sprintf("Image pull started in background (platform: %s).\nTask ID: %s\nUse imageTaskStatus with this task_id to check progress.", platform, taskID)
		}
		return mcp.NewToolResultText(desc), nil
	}

	log.Printf("[INFO] Pulling image: %s, platform: %s", image, platform)
	err := s.dockerClient.PullImage(ctx, image, platform)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to pull image: %v", err)), nil
	}

	result := fmt.Sprintf("Image pulled successfully: %s", image)
	if platform != "" {
		result = fmt.Sprintf("Image pulled successfully: %s (platform: %s)", image, platform)
	}
	return mcp.NewToolResultText(result), nil
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

	// Async mode: start background push and return a task ID immediately
	if request.GetBool("detach", false) {
		taskID := s.startImageTask(ctx, "push", image, func(taskCtx context.Context, task *imageTask) error {
			return s.runPushTask(taskCtx, task, image)
		})
		return mcp.NewToolResultText(fmt.Sprintf(
			"Image push started in background.\nTask ID: %s\nUse imageTaskStatus with this task_id to check progress.", taskID)), nil
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
	"evalscope",
	"git",
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
	userDetach := request.GetBool("detach", false)

	// Auto-detach for long-running commands
	detach := userDetach || isLongRunningCommand(cmdStr)

	// Security check: validate command is allowed
	if allowed, reason := isCommandAllowed(cmdStr); !allowed {
		return mcp.NewToolResultError(fmt.Sprintf("Security rejected: %s", reason)), nil
	}

	// Split command string into slice (by whitespace, not comma)
	cmd := strings.Fields(cmdStr)

	// Detach mode: start exec in background and return exec_id immediately
	if detach {
		execID, err := s.dockerClient.ExecContainerStart(ctx, containerID, cmd, env)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to start exec: %v", err)), nil
		}

		// Track the exec task
		task := &execTask{
			ExecID:      execID,
			ContainerID: containerID,
			Cmd:         cmdStr,
			Status:      taskRunning,
			StartedAt:   time.Now(),
		}
		s.execTaskMu.Lock()
		s.execTasks[execID] = task
		s.execTaskMu.Unlock()

		// Background goroutine: attach and capture output
		go func() {
			bgCtx := context.Background()
			err := s.dockerClient.ExecContainerStream(bgCtx, execID, func(chunk string) {
				s.execTaskMu.Lock()
				task.Output = append(task.Output, chunk)
				if len(task.Output) > 50 {
					task.Output = task.Output[len(task.Output)-50:]
				}
				s.execTaskMu.Unlock()
			})

			// Update task status (don't overwrite if already stopped by user)
			exitCode, _ := s.dockerClient.GetExecExitCode(bgCtx, execID)
			s.execTaskMu.Lock()
			if task.Status == taskRunning {
				task.FinishedAt = time.Now()
				if err != nil {
					task.Status = taskFailed
					task.Error = err.Error()
				} else if exitCode != 0 {
					task.Status = taskFailed
					task.Error = fmt.Sprintf("exit code: %d", exitCode)
				} else {
					task.Status = taskCompleted
				}
			}
			s.execTaskMu.Unlock()
			log.Printf("[INFO] exec task %s finished: %s", execID, task.Status)
		}()

		return mcp.NewToolResultText(fmt.Sprintf(
			"Command started in background.\nExec ID: %s\nCommand: %s\nUse execContainerStatus with this exec_id to check progress.\nUse stopExecCommand with this exec_id to stop the command.",
			execID, cmdStr)), nil
	}

	// Synchronous mode: run and wait for result
	result, err := s.dockerClient.ExecContainer(ctx, containerID, cmd, env, false)
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

	// Check local execTasks first (for async-detached commands)
	s.execTaskMu.Lock()
	task, ok := s.execTasks[execID]
	if ok {
		snapshot := &execTask{
			ExecID:      task.ExecID,
			ContainerID: task.ContainerID,
			Cmd:         task.Cmd,
			Status:      task.Status,
			Output:      append([]string(nil), task.Output...),
			Error:       task.Error,
			StartedAt:   task.StartedAt,
			FinishedAt:  task.FinishedAt,
		}
		s.execTaskMu.Unlock()

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Exec ID: %s\n", snapshot.ExecID))
		sb.WriteString(fmt.Sprintf("Command: %s\n", snapshot.Cmd))
		sb.WriteString(fmt.Sprintf("Status: %s\n", snapshot.Status))
		if !snapshot.StartedAt.IsZero() {
			sb.WriteString(fmt.Sprintf("Started: %s\n", snapshot.StartedAt.Format(time.RFC3339)))
		}
		if snapshot.Status != taskRunning && !snapshot.FinishedAt.IsZero() {
			sb.WriteString(fmt.Sprintf("Finished: %s\n", snapshot.FinishedAt.Format(time.RFC3339)))
		}
		if snapshot.Error != "" {
			sb.WriteString(fmt.Sprintf("Error: %s\n", snapshot.Error))
		}
		if len(snapshot.Output) > 0 {
			sb.WriteString("Output:\n")
			for _, chunk := range snapshot.Output {
				sb.WriteString(chunk)
			}
		}
		return mcp.NewToolResultText(sb.String()), nil
	}
	s.execTaskMu.Unlock()

	// Fall back to Docker API (for execs started without tracking)
	result, err := s.dockerClient.ExecContainerStatus(ctx, execID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get exec status: %v", err)), nil
	}

	output := fmt.Sprintf("Exec ID: %s\n%s", result.ExecID, result.Output)
	return mcp.NewToolResultText(output), nil
}

// newTaskID generates a unique ID for a background image task
func newTaskID(taskType string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%x-%x", taskType, time.Now().UnixNano(), b)
}

// startImageTask registers a task, runs it in a background goroutine, and returns its ID.
// The task is bounded by taskTimeout and kept for taskTTL after completion.
func (s *Server) startImageTask(parent context.Context, taskType, image string, run func(ctx context.Context, task *imageTask) error) string {
	task := &imageTask{
		ID:        newTaskID(taskType),
		Type:      taskType,
		Image:     image,
		Status:    taskRunning,
		StartedAt: time.Now(),
	}

	s.taskMu.Lock()
	s.tasks[task.ID] = task
	s.taskMu.Unlock()

	go func() {
		taskCtx, cancel := context.WithTimeout(parent, taskTimeout)
		defer cancel()

		err := run(taskCtx, task)

		s.taskMu.Lock()
		defer s.taskMu.Unlock()
		task.FinishedAt = time.Now()
		if err != nil {
			task.Status = taskFailed
			task.Error = err.Error()
		} else {
			task.Status = taskCompleted
		}
		log.Printf("[INFO] image task %s (%s) finished: %s", task.Type, task.Image, task.Status)
	}()

	log.Printf("[INFO] image task started: id=%s type=%s image=%s", task.ID, task.Type, task.Image)
	return task.ID
}

// cleanupTasks periodically removes finished tasks older than taskTTL
func (s *Server) cleanupTasks() {
	for {
		time.Sleep(taskCleanupInterval)
		now := time.Now()

		s.taskMu.Lock()
		for id, t := range s.tasks {
			if t.Status != taskRunning && now.Sub(t.FinishedAt) > taskTTL {
				delete(s.tasks, id)
			}
		}
		s.taskMu.Unlock()

		s.execTaskMu.Lock()
		for id, t := range s.execTasks {
			if t.Status != taskRunning && now.Sub(t.FinishedAt) > taskTTL {
				delete(s.execTasks, id)
			}
		}
		s.execTaskMu.Unlock()
	}
}

// normalizePlatform converts shorthand architecture names to full platform strings.
// "amd64", "x86_64", "x86" → "linux/amd64"
// "arm64", "aarch64", "arm" → "linux/arm64"
// Already-qualified platforms like "linux/amd64" are returned as-is.
// Empty input returns empty string (use host default).
func normalizePlatform(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	// Already a full platform (e.g., "linux/amd64")
	if strings.Contains(input, "/") {
		return input
	}
	// Normalize architecture shorthand
	switch strings.ToLower(input) {
	case "amd64", "x86_64", "x86", "x64":
		return "linux/amd64"
	case "arm64", "aarch64", "arm":
		return "linux/arm64"
	case "386", "i386":
		return "linux/386"
	case "armv7", "armv7l", "armhf":
		return "linux/arm/v7"
	default:
		return "linux/" + strings.ToLower(input)
	}
}

// runPullTask executes an image pull in the background, recording progress
func (s *Server) runPullTask(ctx context.Context, task *imageTask, image, platform string) error {
	out, err := s.dockerClient.PullImageStream(ctx, image, platform)
	if err != nil {
		return err
	}
	defer out.Close()
	return s.streamTaskProgress(task, out)
}

// runPushTask executes an image push in the background, recording progress
func (s *Server) runPushTask(ctx context.Context, task *imageTask, image string) error {
	out, err := s.dockerClient.PushImageStream(ctx, image)
	if err != nil {
		return err
	}
	defer out.Close()
	return s.streamTaskProgress(task, out)
}

// streamTaskProgress reads a pull/push progress stream and records recent lines into the task
func (s *Server) streamTaskProgress(task *imageTask, r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var msg map[string]interface{}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if errMsg, ok := msg["error"].(string); ok && errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		if errDetail, ok := msg["errorDetail"].(map[string]interface{}); ok {
			if m, ok := errDetail["message"].(string); ok && m != "" {
				return fmt.Errorf("%s", m)
			}
		}
		s.recordTaskProgress(task, msg)
	}
}

// recordTaskProgress extracts status/progress from a stream message and stores it (capped)
func (s *Server) recordTaskProgress(task *imageTask, msg map[string]interface{}) {
	var line strings.Builder
	if id, ok := msg["id"].(string); ok && id != "" {
		line.WriteString(id)
		line.WriteString(": ")
	}
	if status, ok := msg["status"].(string); ok {
		line.WriteString(status)
	}
	if pd, ok := msg["progressDetail"].(map[string]interface{}); ok {
		if cur, ok := pd["current"].(float64); ok {
			if total, ok := pd["total"].(float64); ok && total > 0 {
				line.WriteString(fmt.Sprintf(" %d/%d (%.1f%%)", int64(cur), int64(total), float64(cur)/float64(total)*100))
			} else {
				line.WriteString(fmt.Sprintf(" %d", int64(cur)))
			}
		}
	}
	if line.Len() == 0 {
		return
	}

	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	task.Progress = append(task.Progress, line.String())
	if len(task.Progress) > 20 {
		task.Progress = task.Progress[len(task.Progress)-20:]
	}
}

// handleImageTaskStatus returns the status and progress of a background image task
func (s *Server) handleImageTaskStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleImageTaskStatus called")
	taskID := request.GetString("task_id", "")
	if taskID == "" {
		return mcp.NewToolResultError("task_id is required"), nil
	}

	s.taskMu.Lock()
	task, ok := s.tasks[taskID]
	if ok {
		// Snapshot under lock to avoid data races
		snapshot := &imageTask{
			ID:         task.ID,
			Type:       task.Type,
			Image:      task.Image,
			Status:     task.Status,
			Progress:   append([]string(nil), task.Progress...),
			Error:      task.Error,
			StartedAt:  task.StartedAt,
			FinishedAt: task.FinishedAt,
		}
		task = snapshot
	}
	s.taskMu.Unlock()

	if !ok {
		return mcp.NewToolResultText(fmt.Sprintf("Task %s not found (never created or already cleaned up)", taskID)), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Task ID: %s\n", task.ID))
	sb.WriteString(fmt.Sprintf("Type: %s\n", task.Type))
	sb.WriteString(fmt.Sprintf("Image: %s\n", task.Image))
	sb.WriteString(fmt.Sprintf("Status: %s\n", task.Status))
	if task.Status != taskRunning {
		sb.WriteString(fmt.Sprintf("Started: %s\n", task.StartedAt.Format(time.RFC3339)))
		sb.WriteString(fmt.Sprintf("Finished: %s\n", task.FinishedAt.Format(time.RFC3339)))
	}
	if task.Error != "" {
		sb.WriteString(fmt.Sprintf("Error: %s\n", task.Error))
	}
	if len(task.Progress) > 0 {
		sb.WriteString("Recent progress:\n")
		for _, l := range task.Progress {
			sb.WriteString("  " + l + "\n")
		}
	}
	return mcp.NewToolResultText(sb.String()), nil
}

// handleStopExecCommand stops a running background exec command
func (s *Server) handleStopExecCommand(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleStopExecCommand called")
	execID := request.GetString("exec_id", "")
	if execID == "" {
		return mcp.NewToolResultError("exec_id is required"), nil
	}

	s.execTaskMu.Lock()
	task, ok := s.execTasks[execID]
	if !ok {
		s.execTaskMu.Unlock()
		return mcp.NewToolResultError(fmt.Sprintf("Exec task %s not found (not tracked or already cleaned up)", execID)), nil
	}

	if task.Status != taskRunning {
		status := task.Status
		s.execTaskMu.Unlock()
		return mcp.NewToolResultText(fmt.Sprintf("Exec %s is not running (status: %s)", execID, status)), nil
	}

	containerID := task.ContainerID
	cmdPattern := task.Cmd
	s.execTaskMu.Unlock()

	// Kill the process in the container
	log.Printf("[INFO] Stopping exec %s in container %s, pattern: %s", execID, containerID, cmdPattern)
	err := s.dockerClient.KillProcessInContainer(ctx, containerID, cmdPattern)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to stop command: %v", err)), nil
	}

	// Update task status
	s.execTaskMu.Lock()
	if task.Status == taskRunning {
		task.Status = taskStopped
		task.FinishedAt = time.Now()
	}
	s.execTaskMu.Unlock()

	return mcp.NewToolResultText(fmt.Sprintf(
		"Command stopped successfully.\nExec ID: %s\nCommand: %s\nNote: modelscope supports resume, you can restart the download later.",
		execID, cmdPattern)), nil
}

// downloadFile downloads a file from a URL to a temporary file and returns its path.
// The caller is responsible for removing the temp file when done.
func downloadFile(url string) (string, error) {
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "docker-image-*.tar")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}

	_, err = io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}

	return tmpFile.Name(), nil
}

// runLoadTask executes download+load+tag+push in the background, recording progress
func (s *Server) runLoadTask(ctx context.Context, task *imageTask, tarURL, targetImage string) error {
	// Step 1: Download tar file
	s.recordTaskProgress(task, map[string]interface{}{"status": "Downloading tar from " + tarURL})
	tarPath, err := downloadFile(tarURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer os.Remove(tarPath)
	s.recordTaskProgress(task, map[string]interface{}{"status": "Download complete"})

	// Step 2: Load image from tar
	s.recordTaskProgress(task, map[string]interface{}{"status": "Loading image from tar..."})
	loadedImage, err := s.dockerClient.LoadImage(ctx, tarPath)
	if err != nil {
		return fmt.Errorf("load failed: %w", err)
	}
	s.recordTaskProgress(task, map[string]interface{}{"status": "Loaded image: " + loadedImage})

	// Step 3: Tag the loaded image
	s.recordTaskProgress(task, map[string]interface{}{"status": "Tagging as " + targetImage})
	err = s.dockerClient.TagImage(ctx, loadedImage, targetImage)
	if err != nil {
		return fmt.Errorf("tag failed: %w", err)
	}
	s.recordTaskProgress(task, map[string]interface{}{"status": "Tagged successfully"})

	// Step 4: Push the tagged image
	s.recordTaskProgress(task, map[string]interface{}{"status": "Pushing " + targetImage})
	out, err := s.dockerClient.PushImageStream(ctx, targetImage)
	if err != nil {
		return fmt.Errorf("push failed: %w", err)
	}
	defer out.Close()
	return s.streamTaskProgress(task, out)
}

// handleLoadImageFromTar downloads a tar, loads it, tags it, and pushes to registry
func (s *Server) handleLoadImageFromTar(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleLoadImageFromTar called")
	tarURL := request.GetString("tar_url", "")
	if tarURL == "" {
		return mcp.NewToolResultError("tar_url is required"), nil
	}

	targetImage := request.GetString("target_image", "")
	if targetImage == "" {
		return mcp.NewToolResultError("target_image is required"), nil
	}

	// Async mode: start background task and return task ID immediately
	if request.GetBool("detach", false) {
		taskID := s.startImageTask(ctx, "load", targetImage, func(taskCtx context.Context, task *imageTask) error {
			return s.runLoadTask(taskCtx, task, tarURL, targetImage)
		})
		return mcp.NewToolResultText(fmt.Sprintf(
			"Image load started in background.\nTask ID: %s\nUse imageTaskStatus with this task_id to check progress.", taskID)), nil
	}

	// Synchronous mode: download, load, tag, push
	tarPath, err := downloadFile(tarURL)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Download failed: %v", err)), nil
	}
	defer os.Remove(tarPath)

	loadedImage, err := s.dockerClient.LoadImage(ctx, tarPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Load failed: %v", err)), nil
	}

	err = s.dockerClient.TagImage(ctx, loadedImage, targetImage)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Tag failed: %v", err)), nil
	}

	err = s.dockerClient.PushImage(ctx, targetImage)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Push failed: %v", err)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Image loaded and pushed successfully.\nLoaded: %s\nTagged as: %s\nPushed to: %s",
		loadedImage, targetImage, targetImage)), nil
}

// githubRelease represents a GitHub release from the API response
type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	PreRelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
}

// httpClientWithProxy creates an HTTP client that respects proxy env vars
func httpClientWithProxy() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
}

// githubAPIRequest makes an authenticated request to GitHub API with optional token
func githubAPIRequest(url string) (*http.Response, error) {
	client := httpClientWithProxy()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

// fetchGitHubReleases fetches releases for a repo (owner/repo format)
func fetchGitHubReleases(repo string) ([]githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=10", repo)
	resp, err := githubAPIRequest(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("repository '%s' not found", repo)
	}
	if resp.StatusCode == 403 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API rate limit exceeded or forbidden: %s", string(body))
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to parse releases: %w", err)
	}
	return releases, nil
}

// fetchRawFile fetches a raw file from GitHub, trying main then master branch
func fetchRawFile(repo, path string) (string, error) {
	branches := []string{"main", "master"}
	var lastErr error
	for _, branch := range branches {
		url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", repo, branch, path)
		resp, err := githubAPIRequest(url)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				lastErr = err
				continue
			}
			return string(body), nil
		}
		lastErr = fmt.Errorf("status %d from %s", resp.StatusCode, url)
	}
	return "", lastErr
}

func (s *Server) handleCheckGitHubRelease(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Printf("[INFO] handleCheckGitHubRelease called")

	repo := request.GetString("repo", "")
	if repo == "" {
		return mcp.NewToolResultError("repo is required (e.g., sgl-project/sglang)"), nil
	}

	currentVersion := request.GetString("current_version", "")
	includeRoadmap := request.GetBool("include_roadmap", true)

	// Fetch releases
	releases, err := fetchGitHubReleases(repo)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to fetch releases: %v", err)), nil
	}

	if len(releases) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf("No releases found for %s", repo)), nil
	}

	// Build result
	var result strings.Builder
	result.WriteString(fmt.Sprintf("=== GitHub Releases for %s ===\n\n", repo))

	// Filter by current version if provided
	newerOnly := currentVersion != ""
	if newerOnly {
		result.WriteString(fmt.Sprintf("Current version: %s\n", currentVersion))
	}

	found := false
	for _, r := range releases {
		if r.Draft {
			continue
		}
		if newerOnly && r.TagName == currentVersion {
			break // releases are sorted newest-first, stop here
		}

		label := ""
		if r.PreRelease {
			label = " [pre-release]"
		}
		result.WriteString(fmt.Sprintf("## %s%s\n", r.TagName, label))
		result.WriteString(fmt.Sprintf("Published: %s\n", r.PublishedAt))
		result.WriteString(fmt.Sprintf("URL: %s\n", r.HTMLURL))
		if r.Body != "" {
			// Truncate very long release notes
			notes := r.Body
			if len(notes) > 2000 {
				notes = notes[:2000] + "\n... (truncated, see full notes at URL above)"
			}
			result.WriteString(fmt.Sprintf("Release Notes:\n%s\n", notes))
		} else {
			result.WriteString("Release Notes: (none provided)\n")
		}
		result.WriteString("\n")
		found = true
	}

	if newerOnly && !found {
		result.WriteString(fmt.Sprintf("No new releases found after %s\n", currentVersion))
	}

	// Fetch roadmap if requested
	if includeRoadmap {
		result.WriteString("\n=== Roadmap ===\n")
		roadmapPaths := []string{
			"docs/references/roadmap.rst",
			"docs/roadmap.md",
			"ROADMAP.md",
		}
		roadmapFound := false
		for _, path := range roadmapPaths {
			content, err := fetchRawFile(repo, path)
			if err == nil && content != "" {
				// Truncate very long roadmaps
				if len(content) > 3000 {
					content = content[:3000] + "\n... (truncated)"
				}
				result.WriteString(fmt.Sprintf("(Source: %s)\n%s\n", path, content))
				roadmapFound = true
				break
			}
		}
		if !roadmapFound {
			result.WriteString("No roadmap file found in common locations (docs/references/roadmap.rst, docs/roadmap.md, ROADMAP.md)\n")
		}
	}

	return mcp.NewToolResultText(result.String()), nil
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
				"description": "Pull an image from registry. Set detach=true for large images to run in background and poll with imageTaskStatus. Use platform to pull a specific architecture (e.g., linux/amd64, linux/arm64).",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"image":    map[string]interface{}{"type": "string", "description": "Image name to pull"},
						"platform": map[string]interface{}{"type": "string", "description": "Target platform (e.g., linux/amd64, linux/arm64). Shorthand: amd64/x86_64/arm64/aarch64 also accepted. Empty = host default."},
						"detach":   map[string]interface{}{"type": "boolean", "description": "Run in background and return a task ID immediately (default: false)"},
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
				"description": "Push an image to registry. Set detach=true for large images to run in background and poll with imageTaskStatus.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"image":  map[string]interface{}{"type": "string", "description": "Image name to push"},
						"detach": map[string]interface{}{"type": "boolean", "description": "Run in background and return a task ID immediately (default: false)"},
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
				"description": "Execute a command in a running container. Long-running commands (modelscope, wget, curl, download, etc.) will auto-detach and return an exec_id immediately. Use execContainerStatus to check progress and stopExecCommand to stop.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"container_id": map[string]interface{}{"type": "string", "description": "Container ID or name"},
						"cmd":          map[string]interface{}{"type": "string", "description": "Command to execute"},
						"env":          map[string]interface{}{"type": "string", "description": "Environment variables (e.g., HTTP_PROXY=http://proxy:8080)"},
						"detach":       map[string]interface{}{"type": "boolean", "description": "Run in background and return exec_id immediately (default: false). Long-running commands auto-detach."},
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
			{
				"name":        "imageTaskStatus",
				"description": "Check the status and progress of a background image task started with pullImage, pushImage, or loadImageFromTar with detach=true",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"task_id": map[string]interface{}{"type": "string", "description": "Task ID returned from pullImage/pushImage/loadImageFromTar with detach=true"},
					},
					"required": []string{"task_id"},
				},
			},
			{
				"name":        "stopExecCommand",
				"description": "Stop a running background exec command (e.g., modelscope download). Useful for interrupting a model download to free bandwidth. modelscope supports resume, so the download can be restarted later.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"exec_id": map[string]interface{}{"type": "string", "description": "Exec ID returned from execContainer with detach=true"},
					},
					"required": []string{"exec_id"},
				},
			},
			{
				"name":        "loadImageFromTar",
				"description": "Download a Docker image tar from a URL, load it with docker load, tag it, and push to a target registry. Supports detach=true for large images.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"tar_url":      map[string]interface{}{"type": "string", "description": "URL to download the image tar file from"},
						"target_image": map[string]interface{}{"type": "string", "description": "Target image name:tag (e.g., myregistry.com/myimage:v1.0)"},
						"detach":       map[string]interface{}{"type": "boolean", "description": "Run in background and return a task ID immediately (default: false)"},
					},
					"required": []string{"tar_url", "target_image"},
				},
			},
			{
				"name":        "checkGitHubRelease",
				"description": "Check a GitHub repository for new releases, release notes, and roadmap updates. Supports proxy via HTTP_PROXY/HTTPS_PROXY env vars and optional GITHUB_TOKEN for higher rate limits.",
				"inputSchema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"repo":            map[string]interface{}{"type": "string", "description": "GitHub repository in owner/repo format (e.g., sgl-project/sglang)"},
						"current_version": map[string]interface{}{"type": "string", "description": "Current version you have (e.g., v0.3.0). If provided, only shows newer releases."},
						"include_roadmap": map[string]interface{}{"type": "boolean", "description": "Whether to fetch roadmap information (default: true)"},
					},
					"required": []string{"repo"},
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
		case "imageTaskStatus":
			result, err = s.handleImageTaskStatus(ctx, req)
		case "stopExecCommand":
			result, err = s.handleStopExecCommand(ctx, req)
		case "loadImageFromTar":
			result, err = s.handleLoadImageFromTar(ctx, req)
		case "checkGitHubRelease":
			result, err = s.handleCheckGitHubRelease(ctx, req)
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
