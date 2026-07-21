package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-units"
	"github.com/garv2003/code-execution-engine/internal/config"
	"github.com/garv2003/code-execution-engine/internal/models"
)

type LanguageSpec struct {
	Image          string `json:"image"`
	Filename       string `json:"filename"`
	CompileCommand string `json:"compile_command"`
	RunCommand     string `json:"run_command"`
	TimeoutMS      int    `json:"timeout_ms"`
	MemoryMB       int64  `json:"memory_mb"`
	HealthCheck    string `json:"health_check_command"`
}

type LanguageConfig map[string]LanguageSpec

type DockerSandbox struct {
	cli       *client.Client
	cfg       *config.Config
	languages LanguageConfig

	verifiedMu sync.Mutex
	verified   map[string]struct{}
}

func LoadLanguageConfig(configPath string) (LanguageConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var langConfig LanguageConfig
	if err := json.Unmarshal(data, &langConfig); err != nil {
		return nil, err
	}

	return langConfig, nil
}

func NewDockerSandbox(cfg *config.Config, langs LanguageConfig) (*DockerSandbox, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}

	return &DockerSandbox{
		cli:       cli,
		cfg:       cfg,
		languages: langs,
		verified:  make(map[string]struct{}),
	}, nil
}

func (s *DockerSandbox) isVerified(imageName string) bool {
	s.verifiedMu.Lock()
	defer s.verifiedMu.Unlock()
	_, ok := s.verified[imageName]
	return ok
}

func (s *DockerSandbox) markVerified(imageName string) {
	s.verifiedMu.Lock()
	defer s.verifiedMu.Unlock()
	s.verified[imageName] = struct{}{}
}

func (s *DockerSandbox) PrePullImages(ctx context.Context, languages []string) error {
	var pullErrors []string
	for _, lang := range languages {
		spec, exists := s.languages[lang]
		if !exists {
			slog.Warn("Skipping pre-pull for unsupported language", "language", lang)
			continue
		}

		slog.Info("Pre-pulling Docker image", "language", lang, "image", spec.Image)
		reader, err := s.cli.ImagePull(ctx, spec.Image, image.PullOptions{})
		if err != nil {
			slog.Error("Failed to pull image", "image", spec.Image, "error", err)
			pullErrors = append(pullErrors, spec.Image+": "+err.Error())
			continue
		}
		_, _ = io.Copy(io.Discard, reader)
		reader.Close()
		s.markVerified(spec.Image)
	}
	if len(pullErrors) > 0 {
		return errors.New("failed to pull images: " + strings.Join(pullErrors, "; "))
	}
	return nil
}

func (s *DockerSandbox) VerifyRuntimes(ctx context.Context) error {
	var verifyErrors []string
	for lang, spec := range s.languages {
		slog.Info("Verifying runtime image", "language", lang, "image", spec.Image)
		if err := s.ensureImage(ctx, lang, spec.Image); err != nil {
			slog.Error("Failed to ensure runtime image", "language", lang, "error", err)
			verifyErrors = append(verifyErrors, lang+": "+err.Error())
			continue
		}
		if spec.HealthCheck == "" {
			continue
		}
		if err := s.runHealthCheck(ctx, lang, spec); err != nil {
			slog.Error("Runtime health check failed", "language", lang, "error", err)
			verifyErrors = append(verifyErrors, lang+": "+err.Error())
		}
	}
	if len(verifyErrors) > 0 {
		return errors.New("runtime verification failed: " + strings.Join(verifyErrors, "; "))
	}
	return nil
}

func makeTarArchive(filename string, fileContent string, extraFiles map[string]string) (io.Reader, error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	if fileContent != "" && filename != "" {
		hdr := &tar.Header{
			Name: filename,
			Mode: 0644,
			Size: int64(len(fileContent)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(fileContent)); err != nil {
			return nil, err
		}
	}

	for name, content := range extraFiles {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}

func (s *DockerSandbox) Run(ctx context.Context, job *models.Job) (*models.ExecutionResult, error) {
	spec, exists := s.languages[job.Language]
	if !exists {
		return nil, errors.New("unsupported language: " + job.Language)
	}

	var executionCmd string
	if spec.CompileCommand != "" {
		executionCmd = spec.CompileCommand + " && " + spec.RunCommand
	} else {
		executionCmd = spec.RunCommand
	}

	containerConfig := &container.Config{
		Image:        spec.Image,
		Cmd:          []string{"sh", "-c", executionCmd},
		OpenStdin:    true,
		StdinOnce:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   "/app",
	}

	memoryLimit, err := units.RAMInBytes(s.cfg.DockerMemoryLimit)
	if err != nil || memoryLimit <= 0 {
		memoryLimit = 128 * 1024 * 1024
	}
	if spec.MemoryMB > 0 {
		memoryLimit = spec.MemoryMB * 1024 * 1024
	}

	hostConfig := &container.HostConfig{
		Resources: container.Resources{
			Memory:         memoryLimit,
			CPUPeriod:      s.cfg.DockerCPUPeriod,
			CPUQuota:       s.cfg.DockerCPUQuota,
			PidsLimit:      int64Ptr(s.cfg.DockerPidsLimit),
			OomKillDisable: boolPtr(false),
		},
		NetworkMode:    "none",
		CapDrop:        strslice.StrSlice{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		ReadonlyRootfs: s.cfg.DockerReadonlyRootfs,
	}

	if s.cfg.DockerReadonlyRootfs {
		hostConfig.Tmpfs = map[string]string{
			"/tmp": fmt.Sprintf("rw,size=%dm", s.cfg.DockerTmpfsSizeMB),
		}
	}

	if s.cfg.DockerRuntime != "" {
		hostConfig.Runtime = s.cfg.DockerRuntime
	}

	if err := s.ensureImage(ctx, job.Language, spec.Image); err != nil {
		return nil, err
	}

	resp, err := s.cli.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return nil, err
	}
	containerID := resp.ID
	defer func() {
		_ = s.cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
	}()

	tarReader, err := makeTarArchive(spec.Filename, job.Code, job.Files)
	if err != nil {
		return nil, err
	}
	err = s.cli.CopyToContainer(ctx, containerID, "/app", tarReader, container.CopyToContainerOptions{})
	if err != nil {
		return nil, err
	}

	attachResp, err := s.cli.ContainerAttach(ctx, containerID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return nil, err
	}
	defer attachResp.Close()

	startTime := time.Now()
	err = s.cli.ContainerStart(ctx, containerID, container.StartOptions{})
	if err != nil {
		return nil, err
	}

	var maxMemory atomic.Uint64
	statsResp, statsErr := s.cli.ContainerStats(ctx, containerID, true)
	if statsErr == nil {
		go func() {
			defer statsResp.Body.Close()
			decoder := json.NewDecoder(statsResp.Body)
			for {
				var stat struct {
					MemoryStats struct {
						Usage uint64 `json:"usage"`
					} `json:"memory_stats"`
				}
				if err := decoder.Decode(&stat); err != nil {
					break
				}
				if stat.MemoryStats.Usage > maxMemory.Load() {
					maxMemory.Store(stat.MemoryStats.Usage)
				}
			}
		}()
	}

	stdoutBuf := newCappedBuffer(int(s.cfg.MaxOutputBytes))
	stderrBuf := newCappedBuffer(int(s.cfg.MaxOutputBytes))
	outputDone := make(chan error, 1)

	if job.Stdin != "" {
		go func() {
			_, _ = io.WriteString(attachResp.Conn, job.Stdin)
			attachResp.CloseWrite()
		}()
	}

	go func() {
		_, err := stdcopy.StdCopy(stdoutBuf, stderrBuf, attachResp.Reader)
		outputDone <- err
	}()

	statusCh, errCh := s.cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

	var waitErr error
	var waitStatus container.WaitResponse

	select {
	case <-ctx.Done():
		_ = s.cli.ContainerStop(context.Background(), containerID, container.StopOptions{Timeout: intPtr(0)})
		return &models.ExecutionResult{
			ID:      job.ID,
			Timeout: true,
		}, nil
	case err := <-errCh:
		waitErr = err
	case status := <-statusCh:
		waitStatus = status
	}

	if waitErr != nil {
		return nil, waitErr
	}

	<-outputDone
	duration := time.Since(startTime)

	inspect, err := s.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}

	isOom := inspect.State.OOMKilled

	return &models.ExecutionResult{
		ID:              job.ID,
		Stdout:          stdoutBuf.String(),
		Stderr:          stderrBuf.String(),
		ExitCode:        int(waitStatus.StatusCode),
		TimeUsed:        duration,
		OOM:             isOom,
		Timeout:         false,
		MemoryUsed:      int64(maxMemory.Load()),
		OutputTruncated: stdoutBuf.truncated || stderrBuf.truncated,
	}, nil
}

func boolPtr(b bool) *bool    { return &b }
func intPtr(i int) *int       { return &i }
func int64Ptr(i int64) *int64 { return &i }

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string { return c.buf.String() }

func (s *DockerSandbox) ensureImage(ctx context.Context, language string, imageName string) error {
	if s.isVerified(imageName) {
		return nil
	}

	_, _, err := s.cli.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		s.markVerified(imageName)
		return nil
	}

	slog.Info("Pulling missing Docker image", "language", language, "image", imageName)
	reader, pullErr := s.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if pullErr != nil {
		return pullErr
	}
	defer reader.Close()

	if _, copyErr := io.Copy(io.Discard, reader); copyErr != nil {
		return copyErr
	}
	s.markVerified(imageName)
	return nil
}

func (s *DockerSandbox) runHealthCheck(ctx context.Context, language string, spec LanguageSpec) error {
	containerConfig := &container.Config{
		Image:      spec.Image,
		Cmd:        []string{"sh", "-lc", spec.HealthCheck},
		WorkingDir: "/app",
	}

	resp, err := s.cli.ContainerCreate(ctx, containerConfig, &container.HostConfig{
		NetworkMode: "none",
		Resources: container.Resources{
			Memory:   32 * 1024 * 1024,
			NanoCPUs: 100000000,
		},
	}, nil, nil, "")
	if err != nil {
		return err
	}
	containerID := resp.ID
	defer func() {
		_ = s.cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
	}()

	if err := s.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return err
	}

	statusCh, errCh := s.cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			return fmt.Errorf("health check exited with code %d", status.StatusCode)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}
