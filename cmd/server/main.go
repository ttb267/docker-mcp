package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/docker-mcp/docker-mcp/internal/mcp"
)

func main() {
	port := flag.String("port", "8080", "HTTP server port (for HTTP mode)")
	mode := flag.String("mode", "stdio", "Server mode: stdio or http")
	apiKey := flag.String("api-key", os.Getenv("MCP_API_KEY"), "API Key for Authorization header authentication")
	flag.Parse()

	// Pass API key to server if provided
	if *apiKey != "" {
		mcp.SetAPIKey(*apiKey)
	}

	server, err := mcp.NewServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create server: %v\n", err)
		os.Exit(1)
	}
	defer server.Close()

	switch *mode {
	case "http":
		// HTTP mode: stdout logging is fine
		fmt.Printf("Docker MCP Server starting in HTTP mode on port %s...\n", *port)
		if *apiKey != "" {
			fmt.Printf("API Key authentication enabled\n")
		}
		if err := server.RunHTTP(*port); err != nil {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
			os.Exit(1)
		}
	case "stdio":
		// STDIO mode: NEVER write to stdout, use stderr only
		// fmt.Println would corrupt JSON-RPC messages
		fmt.Fprintf(os.Stderr, "Docker MCP Server starting in stdio mode...\n")
		if err := server.RunStdio(); err != nil {
			fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown mode: %s (use 'stdio' or 'http')\n", *mode)
		os.Exit(1)
	}
}
