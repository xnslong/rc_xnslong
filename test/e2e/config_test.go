package e2e_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/internal/config"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// @test-case TC4.1-invalid_config
// Test 4.1: 无效配置边界容错
func TestConfig_PartialAvailability(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid event schema
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "events", "order", "events"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "events", "order", "events", "order.paid.yaml"), []byte(`
event_type: "order.paid"
schema:
  type: object
  properties:
    id:
      type: string
`), 0644))

	// Create a valid route for order.paid â†' good_vendor
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "events", "order", "routes"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "events", "order", "routes", "order.paid.yaml"), []byte(`
event_type: "order.paid"
routes:
  - vendor_id: "good_vendor"
  - vendor_id: "bad_vendor"
`), 0644))

	// Create a valid vendor config
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "vendors", "good_vendor"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "vendors", "good_vendor", "vendor.yaml"), []byte(`
vendor_id: "good_vendor"
request:
  method: POST
  url: "http://example.com/api"
  body:
    type: raw
retry_policy:
  max_attempts: 3
  base_delay: 1s
  max_delay: 10s
  multiplier: 2.0
  jitter: 0.2
`), 0644))

	// Create an invalid vendor config (bad YAML â€” unclosed headers block)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "vendors", "bad_vendor"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "vendors", "bad_vendor", "vendor.yaml"), []byte(`
vendor_id: "bad_vendor"
request:
  method: "POST"
  url: "http://example.com/api"
  headers
    Content-Type: "application/json"
`), 0644))

	loader, err := config.NewLoader(tmpDir)
	require.NoError(t, err)

	// Load should succeed even with invalid vendor config
	err = loader.Load(context.Background())
	require.NoError(t, err, "loader should not fail on invalid vendor YAML")

	// Good vendor should be accessible
	goodVendor, err := loader.GetVendorConfig("good_vendor")
	require.NoError(t, err)
	require.NotNil(t, goodVendor)
	assert.Equal(t, "good_vendor", goodVendor.VendorID)

	// Bad vendor should return loading error
	badVendor, err := loader.GetVendorConfig("bad_vendor")
	assert.Error(t, err, "bad_vendor should return an error")
	assert.Nil(t, badVendor)
	assert.False(t, errors.Is(err, port.ErrNotConfigured),
		"bad_vendor error should be a load error, not not-configured")

	// Good vendor routes should still work
	rules, err := loader.GetRoutingRules("order.paid")
	require.NoError(t, err)
	assert.Len(t, rules, 2, "order.paid should still have both route rules")
}

// @test-case TC4.2-nonexistent-path
// Test 4.2: 完全缺失配置拒绝启动
func TestConfig_NonExistentPath(t *testing.T) {
	loader, err := config.NewLoader("/nonexistent/path")
	require.NoError(t, err)
	require.NotNil(t, loader)

	err = loader.Load(context.Background())
	assert.Error(t, err)
}
