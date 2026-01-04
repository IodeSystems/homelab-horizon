package system

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type FileSystem interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	Stat(path string) (os.FileInfo, error)
	Exists(path string) bool
	Remove(path string) error
	MkdirAll(path string, perm os.FileMode) error
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error)
	Start(ctx context.Context, name string, args ...string) (Process, error)
	LookPath(file string) (string, error)
}

type Process interface {
	Wait() error
	Kill() error
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.Reader, error)
	StderrPipe() (io.Reader, error)
}

type RealFileSystem struct{}

func (fs *RealFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (fs *RealFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (fs *RealFileSystem) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (fs *RealFileSystem) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (fs *RealFileSystem) Remove(path string) error {
	return os.Remove(path)
}

func (fs *RealFileSystem) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

type RealCommandRunner struct{}

func (r *RealCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Run()
}

func (r *RealCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

func (r *RealCommandRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func (r *RealCommandRunner) Start(ctx context.Context, name string, args ...string) (Process, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	return &realProcess{cmd: cmd}, nil
}

func (r *RealCommandRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

type realProcess struct {
	cmd *exec.Cmd
	mu  sync.Mutex
}

func (p *realProcess) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd.Wait()
}

func (p *realProcess) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd.Process.Kill()
}

func (p *realProcess) StdinPipe() (io.WriteCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd.StdinPipe()
}

func (p *realProcess) StdoutPipe() (io.Reader, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd.StdoutPipe()
}

func (p *realProcess) StderrPipe() (io.Reader, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd.StderrPipe()
}

type DryRunFileSystem struct {
	mu      sync.Mutex
	files   map[string][]byte
	written map[string][]byte
	created map[string]bool
	removed map[string]bool
	mkdirs  map[string]bool
}

func NewDryRunFileSystem() *DryRunFileSystem {
	return &DryRunFileSystem{
		files:   make(map[string][]byte),
		written: make(map[string][]byte),
		created: make(map[string]bool),
		removed: make(map[string]bool),
		mkdirs:  make(map[string]bool),
	}
}

func (fs *DryRunFileSystem) ReadFile(path string) ([]byte, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if data, exists := fs.written[path]; exists {
		return data, nil
	}

	if data, exists := fs.files[path]; exists {
		return data, nil
	}

	return os.ReadFile(path)
}

func (fs *DryRunFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.written[path] = data
	return nil
}

func (fs *DryRunFileSystem) Stat(path string) (os.FileInfo, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if _, exists := fs.files[path]; exists {
		return &mockFileInfo{path: path, isDir: false}, nil
	}

	if _, exists := fs.created[path]; exists {
		return &mockFileInfo{path: path, isDir: false}, nil
	}

	if _, exists := fs.mkdirs[path]; exists {
		return &mockFileInfo{path: path, isDir: true}, nil
	}

	return os.Stat(path)
}

func (fs *DryRunFileSystem) Exists(path string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if _, exists := fs.files[path]; exists {
		return true
	}

	if _, exists := fs.created[path]; exists {
		return true
	}

	if _, exists := fs.mkdirs[path]; exists {
		return true
	}

	_, err := os.Stat(path)
	return err == nil
}

func (fs *DryRunFileSystem) Remove(path string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.removed[path] = true
	return nil
}

func (fs *DryRunFileSystem) MkdirAll(path string, perm os.FileMode) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.mkdirs[path] = true
	return nil
}

func (fs *DryRunFileSystem) AddFile(path string, data []byte) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.files[path] = data
}

func (fs *DryRunFileSystem) GetWrittenFiles() map[string][]byte {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	result := make(map[string][]byte)
	for k, v := range fs.written {
		result[k] = v
	}
	return result
}

func (fs *DryRunFileSystem) GetCreatedFiles() map[string]bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	result := make(map[string]bool)
	for k, v := range fs.created {
		result[k] = v
	}
	return result
}

func (fs *DryRunFileSystem) GetRemovedFiles() map[string]bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	result := make(map[string]bool)
	for k, v := range fs.removed {
		result[k] = v
	}
	return result
}

func (fs *DryRunFileSystem) GetCreatedDirs() map[string]bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	result := make(map[string]bool)
	for k, v := range fs.mkdirs {
		result[k] = v
	}
	return result
}

type DryRunCommandRunner struct {
	mu     sync.Mutex
	ran    []string
	output map[string][]byte
	errors map[string]error
}

func NewDryRunCommandRunner() *DryRunCommandRunner {
	return &DryRunCommandRunner{
		ran:    make([]string, 0),
		output: make(map[string][]byte),
		errors: make(map[string]error),
	}
}

func (r *DryRunCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	cmd := append([]string{name}, args...)
	cmdStr := commandString(cmd)
	r.ran = append(r.ran, cmdStr)

	if err, exists := r.errors[cmdStr]; exists {
		return err
	}

	return nil
}

func (r *DryRunCommandRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cmd := append([]string{name}, args...)
	cmdStr := commandString(cmd)
	r.ran = append(r.ran, cmdStr)

	if err, exists := r.errors[cmdStr]; exists {
		return nil, err
	}

	if output, exists := r.output[cmdStr]; exists {
		return output, nil
	}

	return []byte{}, nil
}

func (r *DryRunCommandRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Output(ctx, name, args...)
}

func (r *DryRunCommandRunner) Start(ctx context.Context, name string, args ...string) (Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cmd := append([]string{name}, args...)
	cmdStr := commandString(cmd)
	r.ran = append(r.ran, cmdStr)

	if err, exists := r.errors[cmdStr]; exists {
		return nil, err
	}

	return &mockProcess{}, nil
}

func (r *DryRunCommandRunner) LookPath(file string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = append(r.ran, "lookpath: "+file)
	return "/usr/bin/" + file, nil
}

func (r *DryRunCommandRunner) AddOutput(command string, output []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.output[command] = output
}

func (r *DryRunCommandRunner) AddError(command string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors[command] = err
}

func (r *DryRunCommandRunner) GetRunCommands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ran
}

func (r *DryRunCommandRunner) GetLastCommands(count int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.ran) <= count {
		return r.ran
	}

	return r.ran[len(r.ran)-count:]
}

func (r *DryRunCommandRunner) GetCommandsByType(cmdType string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var result []string
	for _, cmd := range r.ran {
		if cmdType == "" || strings.Contains(cmd, cmdType) {
			result = append(result, cmd)
		}
	}
	return result
}

func (r *DryRunCommandRunner) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = r.ran[:0]
	r.output = make(map[string][]byte)
	r.errors = make(map[string]error)
}

func commandString(cmd []string) string {
	result := cmd[0]
	for _, arg := range cmd[1:] {
		result += " " + arg
	}
	return result
}

type mockFileInfo struct {
	path  string
	isDir bool
}

func (fi *mockFileInfo) Name() string       { return fi.path }
func (fi *mockFileInfo) Size() int64        { return 0 }
func (fi *mockFileInfo) Mode() os.FileMode  { return 0644 }
func (fi *mockFileInfo) ModTime() time.Time { return time.Time{} }
func (fi *mockFileInfo) Sys() any           { return nil }
func (fi *mockFileInfo) IsDir() bool        { return fi.isDir }

type mockFile struct {
	path string
}

func (f *mockFile) Read(p []byte) (int, error)  { return 0, io.EOF }
func (f *mockFile) Write(p []byte) (int, error) { return len(p), nil }
func (f *mockFile) Close() error                { return nil }
func (f *mockFile) Stat() (os.FileInfo, error)  { return &mockFileInfo{path: f.path}, nil }

type mockProcess struct{}

func (p *mockProcess) Wait() error                        { return nil }
func (p *mockProcess) Kill() error                        { return nil }
func (p *mockProcess) StdinPipe() (io.WriteCloser, error) { return nil, nil }
func (p *mockProcess) StdoutPipe() (io.Reader, error)     { return nil, nil }
func (p *mockProcess) StderrPipe() (io.Reader, error)     { return nil, nil }
