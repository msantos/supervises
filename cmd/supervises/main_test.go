package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "supervises-test-*")
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	binaryPath = filepath.Join(tmpDir, "supervises")
	// Compile the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

func TestStrategyAlways_Success(t *testing.T) {
	// strategy = always, exit status = 0, restart-count = 2
	// Should run twice, output two lines, and exit with code 0.
	cmd := exec.Command(binaryPath, "-strategy", "always", "-restart-count", "2", "-restart-wait", "1ms", "@echo hello")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("supervises failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	count := strings.Count(output, "hello")
	if count != 2 {
		t.Errorf("expected command to run 2 times, ran %d times. stdout: %s", count, output)
	}
}

func TestStrategyAlways_Failure(t *testing.T) {
	// strategy = always, exit status = 1, restart-count = 2
	// Should run twice, output two lines, and exit with code 1.
	cmd := exec.Command(binaryPath, "-strategy", "always", "-restart-count", "2", "-restart-wait", "1ms", "@echo hello; exit 1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected supervises to fail, but it exited with 0")
	}

	// Verify it exited with 1
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit status 1, got %d", exitErr.ExitCode())
	}

	output := stdout.String()
	count := strings.Count(output, "hello")
	if count != 2 {
		t.Errorf("expected command to run 2 times, ran %d times. stdout: %s", count, output)
	}
}

func TestStrategyOnError_Success(t *testing.T) {
	// strategy = on-error, exit status = 0
	// Should run exactly once and exit with code 0 (since success stops it).
	cmd := exec.Command(binaryPath, "-strategy", "on-error", "-restart-count", "3", "-restart-wait", "1ms", "@echo hello")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("supervises failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	count := strings.Count(output, "hello")
	if count != 1 {
		t.Errorf("expected command to run 1 time (stopped on success), ran %d times. stdout: %s", count, output)
	}
}

func TestStrategyOnError_Failure(t *testing.T) {
	// strategy = on-error, exit status = 1, restart-count = 2
	// Should run twice (restart on error) and exit with code 1.
	cmd := exec.Command(binaryPath, "-strategy", "on-error", "-restart-count", "2", "-restart-wait", "1ms", "@echo hello; exit 1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected supervises to fail, but it exited with 0")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit status 1, got %d", exitErr.ExitCode())
	}

	output := stdout.String()
	count := strings.Count(output, "hello")
	if count != 2 {
		t.Errorf("expected command to run 2 times, ran %d times. stdout: %s", count, output)
	}
}

func TestStrategyOnSuccess_Success(t *testing.T) {
	// strategy = on-success, exit status = 0, restart-count = 2
	// Should run twice (restart on success) and exit with code 0.
	cmd := exec.Command(binaryPath, "-strategy", "on-success", "-restart-count", "2", "-restart-wait", "1ms", "@echo hello")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("supervises failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	count := strings.Count(output, "hello")
	if count != 2 {
		t.Errorf("expected command to run 2 times, ran %d times. stdout: %s", count, output)
	}
}

func TestStrategyOnSuccess_Failure(t *testing.T) {
	// strategy = on-success, exit status = 1
	// Should run exactly once and exit with code 1 (since error stops it).
	cmd := exec.Command(binaryPath, "-strategy", "on-success", "-restart-count", "3", "-restart-wait", "1ms", "@echo hello; exit 1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected supervises to fail, but it exited with 0")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit status 1, got %d", exitErr.ExitCode())
	}

	output := stdout.String()
	count := strings.Count(output, "hello")
	if count != 1 {
		t.Errorf("expected command to run 1 time (stopped on failure), ran %d times. stdout: %s", count, output)
	}
}
