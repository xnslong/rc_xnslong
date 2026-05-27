package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/internal/config"
)

// @test-case TC4.1-invalid_config
// Test 1.3.1: 无效配置拒绝启动
func TestConfig_InvalidConfigRejectsStartup(t *testing.T) {
	// Create temp directory with invalid YAML
	tmpDir, err := os.MkdirTemp("", "e2e-invalid-config-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create a bad vendor config (invalid YAML)
	badDir := filepath.Join(tmpDir, "vendors")
	require.NoError(t, os.MkdirAll(badDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(badDir, "bad_vendor.yaml"), []byte(`
vendor_id: "bad_vendor"
request:
  method: "POST"
  url: "http://example.com/api"
  headers
    Content-Type: "application/json"
`), 0644))

	// Attempt to load config
	loader, err := config.NewLoader(tmpDir)
	require.NoError(t, err)

	err = loader.Load(context.Background())
	assert.Error(t, err, "config loader should fail on invalid YAML")
}
