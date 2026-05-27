package config_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xnslong/rc_xnslong/internal/config"
)

// TestConfigLoader_GetRoutingRules verifies that GetRoutingRules returns
// routing rules for a given event type.
//
// Currently Loader.GetRoutingRules is a stub (returns nil). When Load is
// implemented, this test should provide a routing_rules.yaml file, call
// Load, and verify that GetRoutingRules("order.paid") returns the correct
// vendor list: [crm_system, ad_platform].
func TestConfigLoader_GetRoutingRules(t *testing.T) {
	loader, err := config.NewLoader("testdata/routing_rules.yaml")
	require.NoError(t, err)
	require.NotNil(t, loader)

	// Stub: GetRoutingRules currently returns nil.
	rules := loader.GetRoutingRules("order.paid")
	assert.Empty(t, rules, "GetRoutingRules should return empty slice for stub")
}

// TestConfigLoader_GetVendorConfig verifies that GetVendorConfig returns
// the full vendor configuration including URL, Headers, and Retry policy.
//
// Currently Loader.GetVendorConfig is a stub (returns nil, false). When Load
// is implemented, this test should provide a vendors/crm_system.yaml file,
// call Load, and verify that GetVendorConfig("crm_system") returns a complete
// VendorConfig with the correct URL template, headers, and retry policy.
func TestConfigLoader_GetVendorConfig(t *testing.T) {
	loader, err := config.NewLoader("testdata/vendors/crm_system.yaml")
	require.NoError(t, err)
	require.NotNil(t, loader)

	// Stub: GetVendorConfig currently returns nil, false.
	vendor, ok := loader.GetVendorConfig("crm_system")
	assert.False(t, ok, "GetVendorConfig should return ok=false for stub")
	assert.Nil(t, vendor, "GetVendorConfig should return nil for stub")
}

// TestConfigLoader_Load_Error verifies that Load returns an error when
// the configuration path does not exist.
//
// Currently Loader.Load is a stub (returns nil). When Load is implemented,
// this test should provide a non-existent path and assert that Load returns
// a non-nil error.
func TestConfigLoader_Load_Error(t *testing.T) {
	loader, err := config.NewLoader("/nonexistent/path/config.yaml")
	require.NoError(t, err)
	require.NotNil(t, loader)

	// Stub: Load currently returns nil even for invalid paths.
	// When Load is implemented, this should return an error.
	err = loader.Load(context.Background())
	assert.Error(t, err)
}
