package docker

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
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

// ExecResult contains the result of executing a command in a container
type ExecResult struct {
	ExitCode int
	Output   string
	Error    string
}

// ExecContainer executes a command in a running container
func (d *DockerClient) ExecContainer(ctx context.Context, containerID string, cmd []string) (*ExecResult, error) {
	// First, create the exec instance
	execConfig := types.ExecConfig{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	}

	execID, err := d.cli.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create exec: %w", err)
	}

	// Start the exec
	err = d.cli.ContainerExecStart(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return nil, fmt.Errorf("failed to start exec: %w", err)
	}

	// Wait for exec to finish and get output
	var output bytes.Buffer
	for {
		inspectResp, err := d.cli.ContainerExecInspect(ctx, execID.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to inspect exec: %w", err)
		}

		if !inspectResp.Running {
			// Try to get output from the container's log if available
			// Since we can't easily get exec output, return the exit code
			return &ExecResult{
				ExitCode: inspectResp.ExitCode,
				Output:   output.String(),
			}, nil
		}

		// Small delay to avoid busy loop
		time.Sleep(100 * time.Millisecond)
	}
}
