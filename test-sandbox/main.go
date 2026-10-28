package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func generateSandboxProfile() string {
	return "(version 1) (deny default) (allow process-exec (literal \"/bin/bash\")) (allow process-exec (literal \"/bin/sh\")) (allow file-read* (subpath \"/Users/makkalot/src/caretz\")) (allow file-write* (subpath \"/Users/makkalot/src/caretz\")) (deny network*) (deny process-fork)"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run test-sandbox.go \"command to run\"")
		os.Exit(1)
	}

	command := strings.Join(os.Args[1:], " ")
	fmt.Printf("Running command: %s\n", command)

	sandboxProfile := generateSandboxProfile()
	fmt.Printf("Profile: %s\n", sandboxProfile)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use sh -c to invoke sandbox-exec
	cmd := exec.CommandContext(ctx, "sh", "-c", "sandbox-exec -p '"+sandboxProfile+"' -- bash -c '"+command+"'")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	fmt.Printf("Exit error: %v\n", err)
	fmt.Printf("Stdout: %q\n", stdout.String())
	fmt.Printf("Stderr: %q\n", stderr.String())

	if ctx.Err() == context.DeadlineExceeded {
		fmt.Println("TIMEOUT: Command exceeded 30 seconds")
	}
}
