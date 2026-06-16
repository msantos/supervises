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

func TestStrategyOneForAll_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "log.txt")

	// cmd1 writes, sleeps
	// cmd2 writes, sleeps, exits 1
	// We use -restart-count 2 so it runs twice.
	cmd := exec.Command(binaryPath,
		"-strategy", "one-for-all",
		"-restart-count", "2",
		"-restart-wait", "10ms",
		"@echo cmd1 >> "+logFile+"; sleep 10",
		"@sleep 0.1; echo cmd2 >> "+logFile+"; exit 1",
	)

	_ = cmd.Run() // Expect exit error since cmd2 exits 1

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	content := string(data)
	count1 := strings.Count(content, "cmd1")
	count2 := strings.Count(content, "cmd2")

	if count1 != 2 {
		t.Errorf("expected cmd1 to run 2 times, got %d. Log content:\n%s", count1, content)
	}
	if count2 != 2 {
		t.Errorf("expected cmd2 to run 2 times, got %d. Log content:\n%s", count2, content)
	}
}

func TestStrategyOneForAll_Success(t *testing.T) {
	// If one-for-all strategy processes succeed (exit 0), the supervisor stops (transient behavior).
	cmd := exec.Command(binaryPath,
		"-strategy", "one-for-all",
		"-restart-count", "3",
		"-restart-wait", "10ms",
		"@echo success1",
		"@sleep 0.1; echo success2",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("supervises failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	count1 := strings.Count(output, "success1")
	count2 := strings.Count(output, "success2")

	// They should each run at most once because success stops the supervisor.
	if count1 > 1 || count2 > 1 {
		t.Errorf("expected commands to run at most once, got success1: %d, success2: %d. stdout: %s", count1, count2, output)
	}
}

func TestStrategyRestForOne_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "log.txt")

	// cmd1: started first
	// cmd2: started second, exits 1
	// cmd3: started third
	cmd := exec.Command(binaryPath,
		"-strategy", "rest-for-one",
		"-restart-count", "2",
		"-restart-wait", "10ms",
		"@echo cmd1 >> "+logFile+"; sleep 10",
		"@sleep 0.1; echo cmd2 >> "+logFile+"; exit 1",
		"@echo cmd3 >> "+logFile+"; sleep 10",
	)

	_ = cmd.Run()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	content := string(data)
	count1 := strings.Count(content, "cmd1")
	count2 := strings.Count(content, "cmd2")
	count3 := strings.Count(content, "cmd3")

	if count1 != 1 {
		t.Errorf("expected cmd1 to run 1 time (not terminated), got %d. Log content:\n%s", count1, content)
	}
	if count2 != 2 {
		t.Errorf("expected cmd2 to run 2 times, got %d. Log content:\n%s", count2, content)
	}
	if count3 != 2 {
		t.Errorf("expected cmd3 to run 2 times (terminated and restarted), got %d. Log content:\n%s", count3, content)
	}
}

func TestStrategyOneForAll_Success_KeepsOthers(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "log.txt")

	// cmd1 exits 0 immediately
	// cmd2 runs for 0.2s then writes to file and exits 0
	cmd := exec.Command(binaryPath,
		"-strategy", "one-for-all",
		"-restart-wait", "10ms",
		"@echo cmd1 >> "+logFile,
		"@sleep 0.2; echo cmd2 >> "+logFile,
	)

	err := cmd.Run()
	if err != nil {
		t.Fatalf("supervises failed: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "cmd1") || !strings.Contains(content, "cmd2") {
		t.Errorf("expected both cmd1 and cmd2 to run to completion, got log content:\n%s", content)
	}
}

func TestStrategyOnError_Success_KeepsOthers(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "log.txt")

	// cmd1 exits 0 immediately
	// cmd2 runs for 0.2s then writes to file and exits 0
	cmd := exec.Command(binaryPath,
		"-strategy", "on-error",
		"-restart-wait", "10ms",
		"@echo cmd1 >> "+logFile,
		"@sleep 0.2; echo cmd2 >> "+logFile,
	)

	err := cmd.Run()
	if err != nil {
		t.Fatalf("supervises failed: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "cmd1") || !strings.Contains(content, "cmd2") {
		t.Errorf("expected both cmd1 and cmd2 to run to completion under on-error, got log content:\n%s", content)
	}
}

func TestStrategyOneForAllAlways_Success(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "log.txt")

	// cmd1 sleeps for 0.1s, writes, and exits 0 (not a crash)
	// cmd2 writes immediately, and sleeps
	// We use -restart-count 2 so it runs twice.
	cmd := exec.Command(binaryPath,
		"-strategy", "one-for-all-always",
		"-restart-count", "2",
		"-restart-wait", "10ms",
		"@sleep 0.1; echo cmd1 >> "+logFile,
		"@echo cmd2 >> "+logFile+"; sleep 10",
	)

	_ = cmd.Run()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	content := string(data)
	count1 := strings.Count(content, "cmd1")
	count2 := strings.Count(content, "cmd2")

	if count1 != 2 {
		t.Errorf("expected cmd1 to run 2 times, got %d. Log content:\n%s", count1, content)
	}
	if count2 != 2 {
		t.Errorf("expected cmd2 to run 2 times, got %d. Log content:\n%s", count2, content)
	}
}

func TestStrategyRestForOneAlways_Success(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "log.txt")

	// cmd1: started first, sleeps 0.1s, writes, exits 0
	// cmd2: started second, writes immediately, sleeps
	// cmd3: started third, writes immediately, sleeps
	cmd := exec.Command(binaryPath,
		"-strategy", "rest-for-one-always",
		"-restart-count", "2",
		"-restart-wait", "10ms",
		"@sleep 0.1; echo cmd1 >> "+logFile,
		"@echo cmd2 >> "+logFile+"; sleep 10",
		"@echo cmd3 >> "+logFile+"; sleep 10",
	)

	_ = cmd.Run()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	content := string(data)
	count1 := strings.Count(content, "cmd1")
	count2 := strings.Count(content, "cmd2")
	count3 := strings.Count(content, "cmd3")

	if count1 != 2 {
		t.Errorf("expected cmd1 to run 2 times, got %d. Log content:\n%s", count1, content)
	}
	if count2 != 2 {
		t.Errorf("expected cmd2 to run 2 times (terminated and restarted), got %d. Log content:\n%s", count2, content)
	}
	if count3 != 2 {
		t.Errorf("expected cmd3 to run 2 times (terminated and restarted), got %d. Log content:\n%s", count3, content)
	}
}



