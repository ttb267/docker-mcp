package compose

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type ComposeService struct{}

func NewComposeService() *ComposeService {
	return &ComposeService{}
}

type ComposeConfig struct {
	Version  string                 `yaml:"version"`
	Services map[string]ServiceSpec `yaml:"services"`
}

type ServiceSpec struct {
	Image       string            `yaml:"image"`
	Ports       []string          `yaml:"ports,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Command     string            `yaml:"command,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty"`
}

func (s *ComposeService) ParseComposeFile(path string) (*ComposeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read compose file: %w", err)
	}

	var config ComposeConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse compose file: %w", err)
	}

	return &config, nil
}

func (s *ComposeService) Up(ctx context.Context, composeFile string, projectName string) (string, error) {
	absPath, err := filepath.Abs(composeFile)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	dir := filepath.Dir(absPath)

	args := []string{"-f", absPath, "up", "-d"}
	if projectName != "" {
		args = append(args, "-p", projectName)
	}

	cmd := exec.CommandContext(ctx, "docker-compose", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to run docker-compose up: %w", err)
	}

	return "Services started successfully", nil
}

func (s *ComposeService) Down(ctx context.Context, composeFile string, projectName string) (string, error) {
	absPath, err := filepath.Abs(composeFile)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	dir := filepath.Dir(absPath)

	args := []string{"-f", absPath, "down"}
	if projectName != "" {
		args = append(args, "-p", projectName)
	}

	cmd := exec.CommandContext(ctx, "docker-compose", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to run docker-compose down: %w", err)
	}

	return "Services stopped successfully", nil
}

func (s *ComposeService) PS(ctx context.Context, composeFile string, projectName string) (string, error) {
	absPath, err := filepath.Abs(composeFile)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	dir := filepath.Dir(absPath)

	args := []string{"-f", absPath, "ps"}
	if projectName != "" {
		args = append(args, "-p", projectName)
	}

	cmd := exec.CommandContext(ctx, "docker-compose", args...)
	cmd.Dir = dir

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run docker-compose ps: %w", err)
	}

	return string(output), nil
}
