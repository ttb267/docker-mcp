package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type DockerClient struct {
	cli *dockerclient.Client

	// Credentials stored by loginToRegistry, keyed by normalized registry hostname.
	// The docker SDK's ImagePush/ImagePull do NOT read the docker config file,
	// so we must pass auth explicitly via RegistryAuth.
	mu    sync.RWMutex
	auths map[string]registry.AuthConfig
}

type ContainerConfig struct {
	Image string
	Name  string
	Ports []string
	Env   []string
	Cmd   []string
}

type ContainerInfo struct {
	ID      string
	Names   []string
	Image   string
	Status  string
	State   string
	Ports   []types.Port
	Created int64
}

func NewDockerClient() (*DockerClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}
	return &DockerClient{cli: cli, auths: make(map[string]registry.AuthConfig)}, nil
}

func (d *DockerClient) Close() error {
	return d.cli.Close()
}

// Ping checks if Docker daemon is accessible
func (d *DockerClient) Ping(ctx context.Context) error {
	_, err := d.cli.Ping(ctx)
	return err
}

func (d *DockerClient) CreateContainer(ctx context.Context, cfg ContainerConfig) (string, error) {
	portBindings := make(nat.PortMap)
	exposedPorts := make(nat.PortSet)

	for _, p := range cfg.Ports {
		var hostPort, containerPort string
		fmt.Sscanf(p, "%s:%s", &hostPort, &containerPort)
		if containerPort != "" {
			port := nat.Port(containerPort + "/tcp")
			exposedPorts[port] = struct{}{}
			portBindings[port] = []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: hostPort},
			}
		}
	}

	containerCfg := &container.Config{
		Image:        cfg.Image,
		ExposedPorts: exposedPorts,
		Env:          cfg.Env,
		Cmd:          cfg.Cmd,
	}

	hostCfg := &container.HostConfig{
		PortBindings: portBindings,
	}

	resp, err := d.cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, cfg.Name)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("failed to start container: %w", err)
	}

	return resp.ID, nil
}

func (d *DockerClient) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	result := make([]ContainerInfo, 0, len(containers))
	for _, c := range containers {
		names := c.Names
		if len(names) > 0 && names[0][0] == '/' {
			names[0] = names[0][1:]
		}
		result = append(result, ContainerInfo{
			ID:      c.ID,
			Names:   names,
			Image:   c.Image,
			Status:  c.Status,
			State:   c.State,
			Ports:   c.Ports,
			Created: c.Created,
		})
	}

	return result, nil
}

func (d *DockerClient) GetContainerLogs(ctx context.Context, containerID string, tail string) (string, error) {
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tail,
	}

	reader, err := d.cli.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return "", fmt.Errorf("failed to get container logs: %w", err)
	}
	defer reader.Close()

	buf := make([]byte, 1024)
	var logs []byte
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			logs = append(logs, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	return string(logs), nil
}

func (d *DockerClient) InspectContainer(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	info, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return types.ContainerJSON{}, fmt.Errorf("failed to inspect container: %w", err)
	}
	return info, nil
}

// ImageInfo contains information about a Docker image
type ImageInfo struct {
	ID       string
	RepoTags []string
	Size     int64
	Created  int64
}

func (d *DockerClient) ListImages(ctx context.Context) ([]ImageInfo, error) {
	images, err := d.cli.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	result := make([]ImageInfo, 0, len(images))
	for _, img := range images {
		result = append(result, ImageInfo{
			ID:       img.ID,
			RepoTags: img.RepoTags,
			Size:     img.Size,
			Created:  img.Created,
		})
	}
	return result, nil
}

// PullImage pulls an image from registry. platform specifies the target platform
// (e.g., "linux/amd64", "linux/arm64"). Empty string means use default platform.
func (d *DockerClient) PullImage(ctx context.Context, imageName, platform string) error {
	out, err := d.cli.ImagePull(ctx, imageName, image.PullOptions{
		RegistryAuth: d.registryAuthHeader(imageName),
		Platform:     platform,
	})
	if err != nil {
		return fmt.Errorf("failed to pull image: %w", err)
	}
	defer out.Close()

	// Wait for pull to complete by reading all output
	_, err = io.Copy(io.Discard, out)
	return err
}

// TagImage tags an image
func (d *DockerClient) TagImage(ctx context.Context, source, target string) error {
	err := d.cli.ImageTag(ctx, source, target)
	if err != nil {
		return fmt.Errorf("failed to tag image: %w", err)
	}
	return nil
}

// LoginToRegistry logs in to a registry and persists credentials for later push/pull
func (d *DockerClient) LoginToRegistry(ctx context.Context, registryAddr, username, password string) error {
	authConfig := registry.AuthConfig{
		Username:      username,
		Password:      password,
		ServerAddress: registryAddr,
	}

	response, err := d.cli.RegistryLogin(ctx, authConfig)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	// Persist credentials so subsequent push/pull can pass them via RegistryAuth
	d.mu.Lock()
	d.auths[normalizeRegistry(registryAddr)] = authConfig
	d.mu.Unlock()

	fmt.Printf("Login successful: %s\n", response.Status)
	return nil
}

// normalizeRegistry reduces a registry address to its canonical hostname.
// Docker Hub has several aliases, all normalized to "docker.io".
func normalizeRegistry(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.ToLower(addr)
	if i := strings.Index(addr, "://"); i >= 0 {
		addr = addr[i+3:]
	}
	if i := strings.Index(addr, "/"); i >= 0 {
		addr = addr[:i]
	}
	switch addr {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io"
	}
	return addr
}

// authConfigForImage returns the stored auth config for the image's registry
func (d *DockerClient) authConfigForImage(imageName string) registry.AuthConfig {
	domain := ""
	if ref, err := reference.ParseNormalizedNamed(imageName); err == nil {
		domain = normalizeRegistry(reference.Domain(ref))
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	if domain != "" {
		if ac, ok := d.auths[domain]; ok {
			return ac
		}
	}
	return registry.AuthConfig{}
}

// registryAuthHeader returns the base64-encoded X-Registry-Auth value for an image.
// Empty string means no stored credentials (anonymous pull/push).
func (d *DockerClient) registryAuthHeader(imageName string) string {
	ac := d.authConfigForImage(imageName)
	if ac.Username == "" && ac.Password == "" {
		return ""
	}
	enc, err := registry.EncodeAuthConfig(ac)
	if err != nil {
		return ""
	}
	return enc
}

// PushImage pushes an image to registry
func (d *DockerClient) PushImage(ctx context.Context, imageName string) error {
	out, err := d.cli.ImagePush(ctx, imageName, image.PushOptions{RegistryAuth: d.registryAuthHeader(imageName)})
	if err != nil {
		return fmt.Errorf("failed to push image: %w", err)
	}
	defer out.Close()

	// Wait for push to complete
	_, err = io.Copy(io.Discard, out)
	return err
}

// PullImageStream starts pulling an image and returns the progress stream reader.
// platform specifies the target platform (e.g., "linux/amd64", "linux/arm64").
// Empty string means use default platform.
func (d *DockerClient) PullImageStream(ctx context.Context, imageName, platform string) (io.ReadCloser, error) {
	out, err := d.cli.ImagePull(ctx, imageName, image.PullOptions{
		RegistryAuth: d.registryAuthHeader(imageName),
		Platform:     platform,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to pull image: %w", err)
	}
	return out, nil
}

// PushImageStream starts pushing an image and returns the progress stream reader
func (d *DockerClient) PushImageStream(ctx context.Context, imageName string) (io.ReadCloser, error) {
	out, err := d.cli.ImagePush(ctx, imageName, image.PushOptions{RegistryAuth: d.registryAuthHeader(imageName)})
	if err != nil {
		return nil, fmt.Errorf("failed to push image: %w", err)
	}
	return out, nil
}

// ExecResult contains the result of executing a command in a container
type ExecResult struct {
	ExecID   string
	ExitCode int
	Output   string
	Error    string
}

// ExecContainer executes a command in a running container
// env: optional environment variables to pass to the command
// detach: if true, start command in background and return immediately
func (d *DockerClient) ExecContainer(ctx context.Context, containerID string, cmd []string, env []string, detach bool) (*ExecResult, error) {
	// First, create the exec instance
	execConfig := types.ExecConfig{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Env:          env,
	}

	execID, err := d.cli.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create exec: %w", err)
	}

	// If detach mode, stream output in real-time
	if detach {
		err = d.cli.ContainerExecStart(ctx, execID.ID, types.ExecStartCheck{})
		if err != nil {
			return nil, fmt.Errorf("failed to start exec: %w", err)
		}

		// Attach and stream output
		resp, err := d.cli.ContainerExecAttach(ctx, execID.ID, types.ExecStartCheck{
			Tty: false,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to attach exec: %w", err)
		}
		defer resp.Close()

		// Stream output in real-time - read continuously until command finishes
		var output bytes.Buffer
		readChan := make(chan error, 1)

		// Start reading in background
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := resp.Reader.Read(buf)
				if n > 0 {
					output.Write(buf[:n])
					// Keep writing to show progress
					fmt.Fprintf(os.Stdout, "%s", string(buf[:n]))
				}
				if err != nil {
					break
				}
			}
			readChan <- err
		}()

		// Wait for exec to finish
		for {
			inspectResp, err := d.cli.ContainerExecInspect(ctx, execID.ID)
			if err != nil {
				break
			}
			if !inspectResp.Running {
				// Command finished
				return &ExecResult{
					ExecID:   execID.ID,
					ExitCode: inspectResp.ExitCode,
					Output:   output.String(),
				}, nil
			}
			time.Sleep(500 * time.Millisecond)
		}

		return &ExecResult{
			ExecID:   execID.ID,
			ExitCode: -1,
			Output:   output.String(),
		}, nil
	}

	// Start the exec with hijacked connection to get output
	resp, err := d.cli.ContainerExecAttach(ctx, execID.ID, types.ExecStartCheck{
		Tty: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to attach exec: %w", err)
	}
	defer resp.Close()

	// Start the exec
	err = d.cli.ContainerExecStart(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return nil, fmt.Errorf("failed to start exec: %w", err)
	}

	// Read output from hijacked connection's Reader
	output, err := io.ReadAll(resp.Reader)
	if err != nil && err.Error() != "EOF" {
		// Continue even if there's an error, we might still have output
	}

	// Get exit code
	inspectResp, err := d.cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect exec: %w", err)
	}

	return &ExecResult{
		ExitCode: inspectResp.ExitCode,
		Output:   string(output),
	}, nil
}

// ExecContainerStatus checks the status of a detached exec command
func (d *DockerClient) ExecContainerStatus(ctx context.Context, execID string) (*ExecResult, error) {
	inspectResp, err := d.cli.ContainerExecInspect(ctx, execID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect exec: %w", err)
	}

	if inspectResp.Running {
		return &ExecResult{
			ExecID:   execID,
			ExitCode: -1,
			Output:   "Command is still running...",
		}, nil
	}

	return &ExecResult{
		ExecID:   execID,
		ExitCode: inspectResp.ExitCode,
		Output:   fmt.Sprintf("Command finished with exit code: %d", inspectResp.ExitCode),
	}, nil
}

// ExecContainerStart creates and starts an exec instance, returning the exec ID immediately
// without waiting for completion. The caller should use ExecContainerAttachStream to read output.
func (d *DockerClient) ExecContainerStart(ctx context.Context, containerID string, cmd []string, env []string) (string, error) {
	execConfig := types.ExecConfig{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Env:          env,
	}

	execID, err := d.cli.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create exec: %w", err)
	}

	err = d.cli.ContainerExecStart(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return "", fmt.Errorf("failed to start exec: %w", err)
	}

	return execID.ID, nil
}

// ExecContainerStream attaches to a running exec and streams output via a callback.
// Blocks until the exec output stream ends (process exit or EOF).
func (d *DockerClient) ExecContainerStream(ctx context.Context, execID string, onOutput func(string)) error {
	resp, err := d.cli.ContainerExecAttach(ctx, execID, types.ExecStartCheck{
		Tty: false,
	})
	if err != nil {
		return fmt.Errorf("failed to attach exec: %w", err)
	}
	defer resp.Close()

	buf := make([]byte, 4096)
	for {
		n, err := resp.Reader.Read(buf)
		if n > 0 {
			onOutput(string(buf[:n]))
		}
		if err != nil {
			return nil
		}
	}
}

// IsExecRunning checks if an exec instance is still running.
func (d *DockerClient) IsExecRunning(ctx context.Context, execID string) (bool, error) {
	inspectResp, err := d.cli.ContainerExecInspect(ctx, execID)
	if err != nil {
		return false, fmt.Errorf("failed to inspect exec: %w", err)
	}
	return inspectResp.Running, nil
}

// GetExecExitCode returns the exit code of a finished exec.
func (d *DockerClient) GetExecExitCode(ctx context.Context, execID string) (int, error) {
	inspectResp, err := d.cli.ContainerExecInspect(ctx, execID)
	if err != nil {
		return -1, fmt.Errorf("failed to inspect exec: %w", err)
	}
	return inspectResp.ExitCode, nil
}

// KillProcessInContainer kills processes matching the given pattern in a container.
// Uses pkill -f to match the full command line. The pattern should be the original
// command string of the process to kill.
func (d *DockerClient) KillProcessInContainer(ctx context.Context, containerID, pattern string) error {
	execConfig := types.ExecConfig{
		Cmd:          []string{"pkill", "-f", pattern},
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	}

	execID, err := d.cli.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return fmt.Errorf("failed to create kill exec: %w", err)
	}

	err = d.cli.ContainerExecStart(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("failed to start kill exec: %w", err)
	}

	// Wait for kill to complete (brief polling)
	for i := 0; i < 10; i++ {
		inspectResp, err := d.cli.ContainerExecInspect(ctx, execID.ID)
		if err != nil {
			break
		}
		if !inspectResp.Running {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// LoadImage loads a Docker image from a tar file and returns the loaded image name.
func (d *DockerClient) LoadImage(ctx context.Context, tarPath string) (string, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		return "", fmt.Errorf("failed to open tar file: %w", err)
	}
	defer f.Close()

	resp, err := d.cli.ImageLoad(ctx, f, false)
	if err != nil {
		return "", fmt.Errorf("failed to load image: %w", err)
	}
	defer resp.Body.Close()

	// Parse the response to find the loaded image name
	var imageName string
	dec := json.NewDecoder(resp.Body)
	for {
		var msg map[string]interface{}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		if stream, ok := msg["stream"].(string); ok {
			// "Loaded image: <name>\n" or "Loaded image ID: <id>\n"
			if strings.HasPrefix(stream, "Loaded image: ") {
				imageName = strings.TrimSpace(strings.TrimPrefix(stream, "Loaded image: "))
			}
			if strings.HasPrefix(stream, "Loaded image ID: ") {
				imageName = strings.TrimSpace(strings.TrimPrefix(stream, "Loaded image ID: "))
			}
		}
	}

	if imageName == "" {
		return "", fmt.Errorf("failed to determine loaded image name from response")
	}
	return imageName, nil
}
