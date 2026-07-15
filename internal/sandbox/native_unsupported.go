//go:build !linux

package sandbox

import (
	"errors"

	"github.com/garv2003/code-execution-engine/internal/config"
)

// NewNativeSandbox is only implemented on Linux, where namespaces and
// cgroups v2 are available. On other platforms, use SANDBOX_BACKEND=docker.
func NewNativeSandbox(cfg *config.Config, langs LanguageConfig) (Sandbox, error) {
	return nil, errors.New("native sandbox backend requires linux (namespaces/cgroups v2); use SANDBOX_BACKEND=docker on this platform")
}
