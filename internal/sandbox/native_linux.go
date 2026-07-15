//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/go-units"
	"github.com/garv2003/code-execution-engine/internal/config"
	"github.com/garv2003/code-execution-engine/internal/models"
)

// cgroupRoot is the delegated cgroup v2 subtree this process writes limits into.
// The user running the worker must have write access to it (e.g. via systemd
// "Delegate=yes", or by running the worker as root).
const cgroupRoot = "/sys/fs/cgroup/cee"

// nativeAppPath is the path convention used by languages.json commands
// (e.g. "python3 /app/solution.py"), written for the Docker backend where
// /app is the container's WorkingDir. The native backend has no container,
// so this literal path is rewritten to the job's real temp directory.
const nativeAppPath = "/app"

// nativeSandbox executes jobs directly on the host using Linux namespaces
// (PID, mount, network, UTS, IPC) for process/network isolation and cgroups
// v2 for resource limits, instead of spinning up Docker containers.
//
// IMPORTANT — reduced isolation vs. the Docker backend: this does NOT chroot
// or otherwise jail the filesystem, so sandboxed code can still read most of
// the host's filesystem (subject to normal Unix file permissions). It relies
// on language toolchains already being installed on the host, and on cgroup
// v2 + PID/network namespaces to contain resource abuse and network egress.
// Treat this as an experimental, lower-overhead alternative for trusted or
// semi-trusted workloads — not a drop-in security equivalent of the Docker
// (or Docker+gVisor) backend.
type nativeSandbox struct {
	cfg       *config.Config
	languages LanguageConfig
	workDir   string
}

func NewNativeSandbox(cfg *config.Config, langs LanguageConfig) (Sandbox, error) {
	workDir := cfg.NativeWorkDir
	if workDir == "" {
		workDir = filepath.Join(os.TempDir(), "cee-native")
	}
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create native sandbox work dir %s: %w", workDir, err)
	}
	if err := os.MkdirAll(cgroupRoot, 0755); err != nil {
		return nil, fmt.Errorf(
			"failed to create cgroup path %s (requires cgroup v2 delegation or root): %w", cgroupRoot, err)
	}

	slog.Warn("Native sandbox backend is experimental: no filesystem jail is applied; " +
		"sandboxed code can read most host files subject to normal Unix permissions")

	return &nativeSandbox{cfg: cfg, languages: langs, workDir: workDir}, nil
}

func (s *nativeSandbox) Run(ctx context.Context, job *models.Job) (*models.ExecutionResult, error) {
	spec, exists := s.languages[job.Language]
	if !exists {
		return nil, errors.New("unsupported language: " + job.Language)
	}

	jobDir, err := os.MkdirTemp(s.workDir, "job-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create job dir: %w", err)
	}
	defer os.RemoveAll(jobDir)

	if spec.Filename != "" {
		if err := os.WriteFile(filepath.Join(jobDir, spec.Filename), []byte(job.Code), 0644); err != nil {
			return nil, fmt.Errorf("failed to write entrypoint file: %w", err)
		}
	}
	for name, content := range job.Files {
		path := filepath.Join(jobDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, fmt.Errorf("failed to prepare directory for %s: %w", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("failed to write file %s: %w", name, err)
		}
	}

	execCmd := buildNativeCommand(spec, jobDir)

	cgroupPath, cleanupCgroup, err := s.createCgroup(job.ID, spec)
	if err != nil {
		return nil, err
	}
	defer cleanupCgroup()

	cmd := exec.CommandContext(ctx, "sh", "-c", execCmd)
	cmd.Dir = jobDir
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + jobDir}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNET |
			syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWIPC,
	}
	if s.cfg.NativeUID > 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: uint32(s.cfg.NativeUID),
			Gid: uint32(s.cfg.NativeGID),
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	if job.Stdin != "" {
		cmd.Stdin = strings.NewReader(job.Stdin)
	}

	startTime := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start sandboxed process: %w", err)
	}

	if err := s.addToCgroup(cgroupPath, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("failed to attach process to cgroup: %w", err)
	}

	waitErr := cmd.Wait()
	duration := time.Since(startTime)

	oom := s.wasOOMKilled(cgroupPath)

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &models.ExecutionResult{ID: job.ID, Timeout: true}, nil
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if !oom {
			return nil, fmt.Errorf("sandboxed process failed: %w", waitErr)
		}
	}

	return &models.ExecutionResult{
		ID:         job.ID,
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		ExitCode:   exitCode,
		TimeUsed:   duration,
		OOM:        oom,
		MemoryUsed: s.peakMemory(cgroupPath),
	}, nil
}

// buildNativeCommand rewrites the Docker-oriented "/app/..." command strings
// from languages.json to point at the job's real temp directory, since there
// is no container WorkingDir here.
func buildNativeCommand(spec LanguageSpec, jobDir string) string {
	rewrite := func(s string) string { return strings.ReplaceAll(s, nativeAppPath, jobDir) }
	runCmd := rewrite(spec.RunCommand)
	if spec.CompileCommand == "" {
		return runCmd
	}
	return rewrite(spec.CompileCommand) + " && " + runCmd
}

func (s *nativeSandbox) createCgroup(jobID string, spec LanguageSpec) (string, func(), error) {
	cgPath := filepath.Join(cgroupRoot, "job-"+jobID)
	if err := os.MkdirAll(cgPath, 0755); err != nil {
		return "", nil, fmt.Errorf("failed to create cgroup %s: %w", cgPath, err)
	}

	memBytes, err := units.RAMInBytes(s.cfg.DockerMemoryLimit)
	if err != nil || memBytes <= 0 {
		memBytes = 128 * 1024 * 1024
	}
	if spec.MemoryMB > 0 {
		memBytes = spec.MemoryMB * 1024 * 1024
	}
	_ = os.WriteFile(filepath.Join(cgPath, "memory.max"), []byte(strconv.FormatInt(memBytes, 10)), 0644)
	_ = os.WriteFile(filepath.Join(cgPath, "memory.swap.max"), []byte("0"), 0644)
	_ = os.WriteFile(filepath.Join(cgPath, "pids.max"), []byte("128"), 0644)

	if s.cfg.DockerCPUPeriod > 0 && s.cfg.DockerCPUQuota > 0 {
		cpuMax := fmt.Sprintf("%d %d", s.cfg.DockerCPUQuota, s.cfg.DockerCPUPeriod)
		_ = os.WriteFile(filepath.Join(cgPath, "cpu.max"), []byte(cpuMax), 0644)
	}

	cleanup := func() {
		if err := os.Remove(cgPath); err != nil {
			slog.Warn("Failed to remove cgroup after job", "path", cgPath, "error", err)
		}
	}
	return cgPath, cleanup, nil
}

func (s *nativeSandbox) addToCgroup(cgPath string, pid int) error {
	return os.WriteFile(filepath.Join(cgPath, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0644)
}

func (s *nativeSandbox) wasOOMKilled(cgPath string) bool {
	data, err := os.ReadFile(filepath.Join(cgPath, "memory.events"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "oom_kill" {
			n, _ := strconv.Atoi(fields[1])
			return n > 0
		}
	}
	return false
}

func (s *nativeSandbox) peakMemory(cgPath string) int64 {
	data, err := os.ReadFile(filepath.Join(cgPath, "memory.peak"))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n
}
