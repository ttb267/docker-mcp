package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type DockerClient struct {
	cli *client.Client
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
	return &DockerClient{cli: cli}, nil
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

// PullImage pulls an image from registry
func (d *DockerClient) PullImage(ctx context.Context, imageName string) error {
	out, err := d.cli.ImagePull(ctx, imageName, image.PullOptions{})
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

// PushImage pushes an image to registry
func (d *DockerClient) PushImage(ctx context.Context, imageName string) error {
	out, err := d.cli.ImagePush(ctx, imageName, image.PushOptions{})
	if err != nil {
		return fmt.Errorf("failed to push image: %w", err)
	}
	defer out.Close()

	// Wait for push to complete
	_, err = io.Copy(io.Discard, out)
	return err
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
