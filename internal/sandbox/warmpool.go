package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/garv2003/code-execution-engine/internal/models"
)

// warmPool keeps a small number of pre-created, already-started, fully
// hardened containers per language so a job can begin executing without
// paying the ContainerCreate + ContainerStart latency on the critical path.
//
// Exec model
// ----------
// The default create-per-run path bakes the code into a container whose Cmd
// runs it immediately, so that container cannot be reused: by the time we hold
// it, it has already run. A warm container must therefore be started with an
// idle entrypoint ("sleep") and the job's code run inside it via `docker exec`
// (ContainerExecCreate + ContainerExecAttach). All the bounded-output, stdin,
// timeout and memory-stats logic mirrors the create-per-run path.
//
// Isolation trade-off (IMPORTANT)
// -------------------------------
// Each pooled container is used for EXACTLY ONE job and then destroyed; a
// background refill creates its replacement. So a job never shares a container
// with another job's residue — isolation is effectively equivalent to
// create-per-run. The remaining differences vs. the default path are:
//   - the container was started (idle) before the job existed, and the code
//     runs as a `docker exec` child rather than as PID 1 (subtly different
//     signal/reaping semantics);
//   - memory-stats and OOM flags are read from the whole container (the idle
//     `sleep` process is negligible but not zero).
// Because the default path gives the most pristine per-run isolation, the pool
// stays OFF by default and behind WARM_POOL_ENABLED.
//
// NOTE: This path has NOT been exercised against a live Docker daemon in this
// change — it is compile-validated only. It MUST be validated end-to-end with
// a real daemon (create/exec/stats/timeout/OOM/shutdown) before enabling in
// any environment that matters.
type warmPool struct {
	s    *DockerSandbox
	size int

	mu     sync.Mutex
	pools  map[string]chan string // language -> buffered channel of idle container IDs
	closed bool
}

func newWarmPool(s *DockerSandbox, size int) *warmPool {
	return &warmPool{
		s:     s,
		size:  size,
		pools: make(map[string]chan string),
	}
}

// poolFor lazily creates the per-language idle channel.
func (p *warmPool) poolFor(lang string) (chan string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, false
	}
	ch, ok := p.pools[lang]
	if !ok {
		ch = make(chan string, p.size)
		p.pools[lang] = ch
	}
	return ch, true
}

// Warm pre-fills the pool for the given languages so the first jobs also hit a
// ready container. Safe to call in a goroutine; errors are logged, not fatal.
func (p *warmPool) Warm(ctx context.Context, languages []string) {
	for _, lang := range languages {
		spec, exists := p.s.languages[lang]
		if !exists {
			continue
		}
		p.refill(ctx, lang, spec)
	}
}

// refill tops the per-language channel back up to capacity by creating fresh
// idle containers. Channel capacity bounds the number of idle containers, so
// concurrent refills cannot over-provision.
func (p *warmPool) refill(ctx context.Context, lang string, spec LanguageSpec) {
	ch, ok := p.poolFor(lang)
	if !ok {
		return
	}
	for {
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed || len(ch) >= cap(ch) {
			return
		}
		id, err := p.create(ctx, lang, spec)
		if err != nil {
			slog.Warn("Warm pool refill failed", "language", lang, "error", err)
			return
		}
		select {
		case ch <- id:
		default:
			// Raced with other refills and the channel is full; drop the extra.
			p.destroy(id)
			return
		}
	}
}

// create builds and starts one hardened, idle container ready to exec into.
func (p *warmPool) create(ctx context.Context, lang string, spec LanguageSpec) (string, error) {
	if err := p.s.ensureImage(ctx, lang, spec.Image); err != nil {
		return "", err
	}

	containerConfig := &container.Config{
		Image:      spec.Image,
		Cmd:        []string{"sh", "-c", "sleep 2147483647"},
		WorkingDir: "/app",
	}

	resp, err := p.s.cli.ContainerCreate(ctx, containerConfig, p.s.buildHostConfig(spec), nil, nil, "")
	if err != nil {
		return "", err
	}
	if err := p.s.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		p.destroy(resp.ID)
		return "", err
	}
	return resp.ID, nil
}

func (p *warmPool) destroy(id string) {
	_ = p.s.cli.ContainerRemove(context.Background(), id, container.RemoveOptions{Force: true})
}

// acquire returns a ready container ID. It prefers an idle warm container; if
// none is available it falls back to creating one synchronously (correct, just
// without the latency win for that call).
func (p *warmPool) acquire(ctx context.Context, lang string, spec LanguageSpec) (string, error) {
	ch, ok := p.poolFor(lang)
	if !ok {
		return "", context.Canceled
	}
	select {
	case id := <-ch:
		return id, nil
	default:
		return p.create(ctx, lang, spec)
	}
}

// release destroys the just-used container (one job per container) and kicks
// off a background refill so the idle count returns to target.
func (p *warmPool) release(lang string, spec LanguageSpec, id string) {
	p.destroy(id)

	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		p.refill(ctx, lang, spec)
	}()
}

// Close removes every idle container and blocks further pooling.
func (p *warmPool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	pools := p.pools
	p.pools = make(map[string]chan string)
	p.mu.Unlock()

	for _, ch := range pools {
		for {
			select {
			case id := <-ch:
				p.destroy(id)
			default:
				goto next
			}
		}
	next:
	}
}

// runPooled executes a job inside a warm container via `docker exec`, applying
// the same bounded-output, stdin, timeout and memory-stats handling as the
// create-per-run path.
func (s *DockerSandbox) runPooled(ctx context.Context, job *models.Job, spec LanguageSpec, executionCmd string) (*models.ExecutionResult, error) {
	containerID, err := s.pool.acquire(ctx, job.Language, spec)
	if err != nil {
		return nil, err
	}
	defer s.pool.release(job.Language, spec, containerID)

	tarReader, err := makeTarArchive(spec.Filename, job.Code, job.Files)
	if err != nil {
		return nil, err
	}
	if err := s.cli.CopyToContainer(ctx, containerID, "/app", tarReader, container.CopyToContainerOptions{}); err != nil {
		return nil, err
	}

	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", executionCmd},
		AttachStdin:  job.Stdin != "",
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   "/app",
	}
	execResp, err := s.cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return nil, err
	}

	// ContainerExecAttach starts the exec and streams its I/O.
	attachResp, err := s.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{})
	if err != nil {
		return nil, err
	}
	defer attachResp.Close()

	startTime := time.Now()

	// Sample peak memory of the container while the exec runs. Cancelled once
	// output is drained so the stats stream goroutine exits.
	statsCtx, statsCancel := context.WithCancel(ctx)
	defer statsCancel()
	var maxMemory atomic.Uint64
	statsResp, statsErr := s.cli.ContainerStats(statsCtx, containerID, true)
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
		_, copyErr := stdcopy.StdCopy(stdoutBuf, stderrBuf, attachResp.Reader)
		outputDone <- copyErr
	}()

	select {
	case <-ctx.Done():
		// Timeout/cancel: the deferred release force-removes the container,
		// which kills the exec process along with it.
		return &models.ExecutionResult{
			ID:      job.ID,
			Timeout: true,
		}, nil
	case <-outputDone:
	}
	statsCancel()
	duration := time.Since(startTime)

	execInspect, err := s.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return nil, err
	}

	// OOM is read from the container: an over-limit exec child trips the
	// container cgroup's OOM killer.
	isOom := false
	if inspect, inspectErr := s.cli.ContainerInspect(ctx, containerID); inspectErr == nil {
		isOom = inspect.State.OOMKilled
	}

	return &models.ExecutionResult{
		ID:              job.ID,
		Stdout:          stdoutBuf.String(),
		Stderr:          stderrBuf.String(),
		ExitCode:        execInspect.ExitCode,
		TimeUsed:        duration,
		OOM:             isOom,
		Timeout:         false,
		MemoryUsed:      int64(maxMemory.Load()),
		OutputTruncated: stdoutBuf.truncated || stderrBuf.truncated,
	}, nil
}
