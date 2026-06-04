package config_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xnslong/rc_xnslong/internal/config"
)

// TestConfigLoader_GetRoutingRules verifies that GetRoutingRules returns
// routing rules loaded from events/{biz}/route.yaml.
func TestConfigLoader_GetRoutingRules(t *testing.T) {
	loader, err := config.NewLoader("testdata")
	require.NoError(t, err)
	require.NotNil(t, loader)

	err = loader.Load(context.Background())
	require.NoError(t, err)

	rules, err := loader.GetRoutingRules("order.paid")
	require.NoError(t, err)
	require.Len(t, rules, 2, "order.paid should route to 2 vendors")

	// Collect vendor IDs for easier assertion.
	vendorIDs := make([]string, len(rules))
	for i, r := range rules {
		vendorIDs[i] = r.VendorID
	}
	assert.Contains(t, vendorIDs, "crm_system")
	assert.Contains(t, vendorIDs, "ad_platform")
}

// TestConfigLoader_GetVendorConfig verifies that GetVendorConfig returns
// the full vendor configuration loaded from vendors/{vendor}/vendor.yaml.
func TestConfigLoader_GetVendorConfig(t *testing.T) {
	loader, err := config.NewLoader("testdata")
	require.NoError(t, err)
	require.NotNil(t, loader)

	err = loader.Load(context.Background())
	require.NoError(t, err)

	vendor, err := loader.GetVendorConfig("crm_system")
	require.NoError(t, err)
	require.NotNil(t, vendor)

	assert.Equal(t, "crm_system", vendor.VendorID)
	assert.Equal(t, "PATCH", vendor.Request.Method)
	assert.Equal(t, "https://crm.company.com/api/v3/contacts/@{payload.user_id}", vendor.Request.URLTmpl)
	assert.Equal(t, "Bearer crm_api_token_xxx", vendor.Request.Headers["Authorization"])
	assert.Equal(t, 5, vendor.Retry.MaxAttempts)
}

// TestConfigLoader_GetDeliverySpec verifies that GetDeliverySpec merges
// vendor config with delivery contract overrides.
func TestConfigLoader_GetDeliverySpec(t *testing.T) {
	loader, err := config.NewLoader("testdata")
	require.NoError(t, err)
	require.NotNil(t, loader)

	err = loader.Load(context.Background())
	require.NoError(t, err)

	spec, err := loader.GetDeliverySpec("crm_system", "order.paid")
	require.NoError(t, err)
	require.NotNil(t, spec)

	// Method and URL come from vendor defaults.
	assert.Equal(t, "PATCH", spec.Mapping.Request.Method)
	assert.Equal(t, "https://crm.company.com/api/v3/contacts/@{payload.user_id}", spec.Mapping.Request.URLTmpl)

	// Body template comes from the delivery contract.
	assert.Equal(t, "mapping", spec.Mapping.Body.Type)
	require.NotNil(t, spec.Mapping.Body.Template)
	assert.Equal(t, "customer", spec.Mapping.Body.Template["lifecyclestage"])
}

// TestConfigLoader_GetDeliverySpec_NoContract verifies that GetDeliverySpec
// falls back to vendor body config when no contract exists.
func TestConfigLoader_GetDeliverySpec_NoContract(t *testing.T) {
	loader, err := config.NewLoader("testdata")
	require.NoError(t, err)
	require.NotNil(t, loader)

	err = loader.Load(context.Background())
	require.NoError(t, err)

	// "user.registered" has a schema but no routing rule or delivery contract.
	// It should still resolve to vendor defaults.
	spec, err := loader.GetDeliverySpec("ad_platform", "user.registered")
	require.NoError(t, err)
	require.NotNil(t, spec)

	assert.Equal(t, "POST", spec.Mapping.Request.Method)
	assert.Equal(t, "mapping", spec.Mapping.Body.Type)
	assert.Nil(t, spec.Mapping.Body.Template)
}

// TestConfigLoader_GetEventSchema verifies that GetEventSchema returns
// the JSON schema loaded from events/{biz}/events/{event}.yaml.
func TestConfigLoader_GetEventSchema(t *testing.T) {
	loader, err := config.NewLoader("testdata")
	require.NoError(t, err)
	require.NotNil(t, loader)

	err = loader.Load(context.Background())
	require.NoError(t, err)

	schema, err := loader.GetEventSchema("order.paid")
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Contains(t, schema, "type")
	assert.Contains(t, schema, "properties")
}

// TestConfigLoader_Load_Error verifies that Load returns an error when
// the configuration path does not exist.
func TestConfigLoader_Load_Error(t *testing.T) {
	loader, err := config.NewLoader("/nonexistent/path")
	require.NoError(t, err)
	require.NotNil(t, loader)

	err = loader.Load(context.Background())
	assert.Error(t, err)
}
