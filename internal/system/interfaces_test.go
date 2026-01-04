package system

import (
	"context"
	"testing"
)

func TestDryRunFileSystem(t *testing.T) {
	fs := NewDryRunFileSystem()

	_, err := fs.ReadFile("/nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent file")
	}

	testData := []byte("test content")
	fs.AddFile("/test.txt", testData)

	data, err := fs.ReadFile("/test.txt")
	if err != nil {
		t.Fatalf("Unexpected error reading test file: %v", err)
	}

	if string(data) != string(testData) {
		t.Error("File content mismatch")
	}

	err = fs.WriteFile("/written.txt", []byte("written content"), 0644)
	if err != nil {
		t.Fatalf("Unexpected error writing file: %v", err)
	}

	written := fs.GetWrittenFiles()
	if len(written) != 1 {
		t.Errorf("Expected 1 written file, got %d", len(written))
	}

	if string(written["/written.txt"]) != "written content" {
		t.Error("Written content mismatch")
	}

	err = fs.MkdirAll("/test/dir", 0755)
	if err != nil {
		t.Fatalf("Unexpected error creating directory: %v", err)
	}

	dirs := fs.GetCreatedDirs()
	if len(dirs) != 1 {
		t.Errorf("Expected 1 created directory, got %d", len(dirs))
	}

	if !dirs["/test/dir"] {
		t.Error("Expected /test/dir to be created")
	}

	err = fs.Remove("/test.txt")
	if err != nil {
		t.Fatalf("Unexpected error removing file: %v", err)
	}

	removed := fs.GetRemovedFiles()
	if len(removed) != 1 {
		t.Errorf("Expected 1 removed file, got %d", len(removed))
	}

	if !removed["/test.txt"] {
		t.Error("Expected /test.txt to be removed")
	}
}

func TestDryRunCommandRunner(t *testing.T) {
	runner := NewDryRunCommandRunner()

	ctx := context.Background()
	err := runner.Run(ctx, "test", "arg1", "arg2")
	if err != nil {
		t.Errorf("Unexpected error running command: %v", err)
	}

	output, err := runner.Output(ctx, "echo", "hello")
	if err != nil {
		t.Errorf("Unexpected error getting output: %v", err)
	}

	if string(output) != "" {
		t.Errorf("Expected empty output, got %s", string(output))
	}

	runner.AddOutput("echo hello", []byte("hello world"))

	output, err = runner.Output(ctx, "echo", "hello")
	if err != nil {
		t.Errorf("Unexpected error getting output: %v", err)
	}

	if string(output) != "hello world" {
		t.Errorf("Expected 'hello world', got %s", string(output))
	}

	runner.AddError("failing-command", &testError{"command failed"})

	err = runner.Run(ctx, "failing-command")
	if err == nil {
		t.Error("Expected error for failing command")
	}

	path, err := runner.LookPath("test")
	if err != nil {
		t.Errorf("Unexpected error in LookPath: %v", err)
	}

	if path != "/usr/bin/test" {
		t.Errorf("Expected '/usr/bin/test', got %s", path)
	}

	commands := runner.GetRunCommands()
	if len(commands) < 3 {
		t.Errorf("Expected at least 3 commands, got %d", len(commands))
	}

	runner.Clear()
	commands = runner.GetRunCommands()
	if len(commands) != 0 {
		t.Errorf("Expected 0 commands after clear, got %d", len(commands))
	}
}

func TestRealFileSystem(t *testing.T) {
	fs := &RealFileSystem{}

	if !fs.Exists("interfaces.go") {
		t.Error("Expected interfaces.go to exist")
	}

	if fs.Exists("/nonexistent/file/that/should/not/exist") {
		t.Error("Expected non-existent file to not exist")
	}
}

func TestRealCommandRunner(t *testing.T) {
	runner := &RealCommandRunner{}

	path, err := runner.LookPath("go")
	if err != nil {
		t.Errorf("Expected 'go' to be found in PATH: %v", err)
	}

	if path == "" {
		t.Error("Expected non-empty path for 'go' command")
	}

	_, err = runner.LookPath("nonexistent-command-12345")
	if err == nil {
		t.Error("Expected error for non-existent command")
	}
}

func TestCommandString(t *testing.T) {
	tests := []struct {
		cmd  []string
		want string
	}{
		{[]string{"echo", "hello"}, "echo hello"},
		{[]string{"wg", "show", "wg0"}, "wg show wg0"},
		{[]string{"systemctl", "reload", "haproxy"}, "systemctl reload haproxy"},
	}

	for _, tt := range tests {
		got := commandString(tt.cmd)
		if got != tt.want {
			t.Errorf("commandString(%v) = %s, want %s", tt.cmd, got, tt.want)
		}
	}
}

func TestMockFileInfo(t *testing.T) {
	fi := &mockFileInfo{path: "/test/file.txt", isDir: false}

	if fi.Name() != "/test/file.txt" {
		t.Errorf("Expected name '/test/file.txt', got %s", fi.Name())
	}

	if fi.Size() != 0 {
		t.Errorf("Expected size 0, got %d", fi.Size())
	}

	if fi.Mode() != 0644 {
		t.Errorf("Expected mode 0644, got %o", fi.Mode())
	}

	if !fi.ModTime().IsZero() {
		t.Error("Expected zero time for ModTime")
	}

	if fi.Sys() != nil {
		t.Error("Expected nil for Sys")
	}

	if fi.IsDir() {
		t.Error("Expected IsDir() to return false")
	}

	dirFi := &mockFileInfo{path: "/test/dir", isDir: true}
	if !dirFi.IsDir() {
		t.Error("Expected IsDir() to return true for directory")
	}
}

func TestMockProcess(t *testing.T) {
	p := &mockProcess{}

	if err := p.Wait(); err != nil {
		t.Errorf("Expected nil from Wait(), got %v", err)
	}

	if err := p.Kill(); err != nil {
		t.Errorf("Expected nil from Kill(), got %v", err)
	}

	if _, err := p.StdinPipe(); err != nil {
		t.Errorf("Expected nil from StdinPipe(), got %v", err)
	}

	if _, err := p.StdoutPipe(); err != nil {
		t.Errorf("Expected nil from StdoutPipe(), got %v", err)
	}

	if _, err := p.StderrPipe(); err != nil {
		t.Errorf("Expected nil from StderrPipe(), got %v", err)
	}
}

func TestRealProcess(t *testing.T) {
	runner := &RealCommandRunner{}

	proc, err := runner.Start(context.Background(), "sleep", "1")
	if err != nil {
		t.Fatalf("Failed to start sleep process: %v", err)
	}

	if err := proc.Wait(); err != nil {
		t.Errorf("Process wait failed: %v", err)
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
