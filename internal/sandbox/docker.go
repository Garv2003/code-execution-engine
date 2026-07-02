package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/garv2003/code-execution-engine/internal/config"
	"github.com/garv2003/code-execution-engine/internal/models"
)

type LanguageSpec struct {
	Image          string `json:"image"`
	Filename       string `json:"filename"`
	CompileCommand string `json:"compile_command"`
	RunCommand     string `json:"run_command"`
}

type LanguageConfig map[string]LanguageSpec

type DockerSandbox struct {
	cli       *client.Client
	cfg       *config.Config
	languages LanguageConfig
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
	}, nil
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
	}
	if len(pullErrors) > 0 {
		return errors.New("failed to pull images: " + strings.Join(pullErrors, "; "))
	}
	return nil
}

func makeTarArchive(filename string, fileContent string) (io.Reader, error) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	hdr := &tar.Header{
		Name: filename,
		Mode: 0600,
		Size: int64(len(fileContent)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write([]byte(fileContent)); err != nil {
		return nil, err
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

	hostConfig := &container.HostConfig{
		Resources: container.Resources{
			Memory:         128 * 1024 * 1024,
			NanoCPUs:       500000000,
			OomKillDisable: boolPtr(false),
		},
		NetworkMode: "none",
	}

	if s.cfg.DockerMemoryLimit == "128m" {
		hostConfig.Resources.Memory = 128 * 1024 * 1024
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

	tarReader, err := makeTarArchive(spec.Filename, job.Code)
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

	var stdoutBuf, stderrBuf bytes.Buffer
	outputDone := make(chan error, 1)

	if job.Stdin != "" {
		go func() {
			_, _ = io.WriteString(attachResp.Conn, job.Stdin)
			attachResp.CloseWrite()
		}()
	}

	go func() {
		_, err := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, attachResp.Reader)
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
		ID:         job.ID,
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		ExitCode:   int(waitStatus.StatusCode),
		TimeUsed:   duration,
		OOM:        isOom,
		Timeout:    false,
		MemoryUsed: 0,
	}, nil
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

func (s *DockerSandbox) ensureImage(ctx context.Context, language string, imageName string) error {
	_, _, err := s.cli.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		return nil
	}

	slog.Info("Pulling missing Docker image", "language", language, "image", imageName)
	reader, pullErr := s.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if pullErr != nil {
		return pullErr
	}
	defer reader.Close()

	_, copyErr := io.Copy(io.Discard, reader)
	return copyErr
}
