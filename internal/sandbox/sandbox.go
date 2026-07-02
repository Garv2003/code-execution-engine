package sandbox

import (
	"context"

	"github.com/garv2003/code-execution-engine/internal/models"
)

// Sandbox executes a job in an isolated environment and returns its result.
// DockerSandbox and NativeSandbox are the two implementations, selected at
// startup via the SANDBOX_BACKEND config value.
type Sandbox interface {
	Run(ctx context.Context, job *models.Job) (*models.ExecutionResult, error)
}
