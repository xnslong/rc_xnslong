package e2e_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xnslong/rc_xnslong/internal/config"
	"github.com/xnslong/rc_xnslong/internal/port"
)

// getTestdataDir returns the path to the testdata directory.
func getTestdataDir(subdir string) string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "testdata", subdir)
}

// @test-case TC4.1-invalid_config
// Test 4.1: 无效配置边界容错
func TestConfig_PartialAvailability(t *testing.T) {
	configDir := getTestdataDir("tc4_partial")

	loader, err := config.NewLoader(configDir)
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
