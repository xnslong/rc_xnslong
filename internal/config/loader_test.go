package config_test

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
	assert.Equal(t, "https://crm.company.com", vendor.BaseURL)
	assert.Equal(t, "bearer", vendor.Auth.Type)
	assert.Equal(t, "crm_api_token_xxx", vendor.Auth.Config["token"])
	assert.Equal(t, 5, vendor.Retry.MaxAttempts)
}

// TestConfigLoader_GetDeliverySpec verifies that GetDeliverySpec returns
// the delivery contract for a given vendor and event type.
func TestConfigLoader_GetDeliverySpec(t *testing.T) {
	loader, err := config.NewLoader("testdata")
	require.NoError(t, err)
	require.NotNil(t, loader)

	err = loader.Load(context.Background())
	require.NoError(t, err)

	spec, err := loader.GetDeliverySpec("crm_system", "order.paid")
	require.NoError(t, err)
	require.NotNil(t, spec)

	// Method, path and headers come from the delivery contract.
	assert.Equal(t, "PATCH", spec.Mapping.Request.Method)
	assert.Equal(t, "/api/v3/contacts/@{payload:user_id}", spec.Mapping.Request.Path)

	// Body template comes from the delivery contract.
	assert.Equal(t, "mapping", spec.Mapping.Body.Type)
	require.NotNil(t, spec.Mapping.Body.Template)
	assert.Equal(t, "customer", spec.Mapping.Body.Template["lifecyclestage"])
}

// TestConfigLoader_GetDeliverySpec_NoContract verifies that GetDeliverySpec
// returns ErrNotConfigured when no delivery contract exists for the given
// vendor and event type.
func TestConfigLoader_GetDeliverySpec_NoContract(t *testing.T) {
	loader, err := config.NewLoader("testdata")
	require.NoError(t, err)
	require.NotNil(t, loader)

	err = loader.Load(context.Background())
	require.NoError(t, err)

	// "user.registered" has a schema but no routing rule or delivery contract.
	// With the new design, delivery contracts are required — no contract means
	// the spec is not configured.
	spec, err := loader.GetDeliverySpec("ad_platform", "user.registered")
	assert.ErrorIs(t, err, port.ErrNotConfigured)
	assert.Nil(t, spec)
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

// TestConfigLoader_PartialVendorFailure verifies that a bad vendor YAML
// doesn't abort the entire load — Load returns nil, but GetVendorConfig
// returns an error for that vendor.
func TestConfigLoader_PartialVendorFailure(t *testing.T) {
	tmpDir := t.TempDir()

	// events/order/routes/order.paid.yaml
	mustWriteFile(t, tmpDir+"/events/order/routes/order.paid.yaml", `
event_type: "order.paid"
routes:
  - vendor_id: "good_vendor"
  - vendor_id: "bad_vendor"
`)

	// events/order/events/order.paid.yaml (minimal schema)
	mustWriteFile(t, tmpDir+"/events/order/events/order.paid.yaml", `
event_type: "order.paid"
schema:
  type: object
  properties:
    id:
      type: string
`)

	// vendors/good_vendor/vendor.yaml (valid)
	mustWriteFile(t, tmpDir+"/vendors/good_vendor/vendor.yaml", `
vendor_id: "good_vendor"
base_url: "http://example.com/api"
retry_policy:
  max_attempts: 3
  base_delay: 1s
  max_delay: 10s
  multiplier: 2.0
  jitter: 0.2
`)

	// vendors/bad_vendor/vendor.yaml (invalid YAML — unclosed map)
	mustWriteFile(t, tmpDir+"/vendors/bad_vendor/vendor.yaml", `
vendor_id: "bad_vendor"
base_url: "http://example.com/api"
auth
    type: bearer
    config:
      token: "test"
`)

	loader, err := config.NewLoader(tmpDir)
	require.NoError(t, err)

	err = loader.Load(context.Background())
	require.NoError(t, err, "Load should not fail on partial vendor error")

	// Good vendor should be available
	goodVendor, err := loader.GetVendorConfig("good_vendor")
	require.NoError(t, err)
	require.NotNil(t, goodVendor)
	assert.Equal(t, "good_vendor", goodVendor.VendorID)

	// Bad vendor YAML fails to parse; walkYAML skips it; ErrNotConfigured.
	badVendor, err := loader.GetVendorConfig("bad_vendor")
	assert.Error(t, err, "bad_vendor should have load error")
	assert.Nil(t, badVendor)
}

// TestConfigLoader_PartialRouteFailure verifies that a bad route file
// doesn't affect other event types.
func TestConfigLoader_PartialRouteFailure(t *testing.T) {
	tmpDir := t.TempDir()

	// events/order/routes/order.paid.yaml (valid)
	mustWriteFile(t, tmpDir+"/events/order/routes/order.paid.yaml", `
event_type: "order.paid"
routes:
  - vendor_id: "crm_system"
`)

	// events/order/routes/order.bad.yaml (invalid)
	mustWriteFile(t, tmpDir+"/events/order/routes/order.bad.yaml", `
this is not valid yaml: broken
  - no_event_type:
`)

	// events/user/routes/user.registered.yaml (valid, different biz)
	mustWriteFile(t, tmpDir+"/events/user/routes/user.registered.yaml", `
event_type: "user.registered"
routes:
  - vendor_id: "ad_platform"
`)

	// Need vendors referenced by routes
	mustWriteFile(t, tmpDir+"/vendors/crm_system/vendor.yaml", `
vendor_id: "crm_system"
base_url: "http://example.com/api"
retry_policy:
  max_attempts: 3
  base_delay: 1s
  max_delay: 10s
  multiplier: 2.0
  jitter: 0.2
`)
	mustWriteFile(t, tmpDir+"/vendors/ad_platform/vendor.yaml", `
vendor_id: "ad_platform"
base_url: "http://example.com/api"
retry_policy:
  max_attempts: 3
  base_delay: 1s
  max_delay: 10s
  multiplier: 2.0
  jitter: 0.2
`)

	loader, err := config.NewLoader(tmpDir)
	require.NoError(t, err)

	err = loader.Load(context.Background())
	require.NoError(t, err, "Load should not fail on partial route error")

	// Valid event type should have routes
	paidRules, err := loader.GetRoutingRules("order.paid")
	require.NoError(t, err)
	assert.Len(t, paidRules, 1)

	// Valid event from different biz should work
	registeredRules, err := loader.GetRoutingRules("user.registered")
	require.NoError(t, err)
	assert.Len(t, registeredRules, 1)

	// Event with failed route file should return parse error via recordError
	badRules, err := loader.GetRoutingRules("order.bad")
	assert.Error(t, err, "order.bad should have parse error")
	assert.Nil(t, badRules)
	assert.False(t, errors.Is(err, port.ErrNotConfigured),
		"parse error should not be ErrNotConfigured")
}

// TestConfigLoader_CrossConfigValidation verifies that cross-config
// validation (e.g., route references non-existent vendor) is best-effort
// and doesn't block startup.
func TestConfigLoader_CrossConfigValidation(t *testing.T) {
	tmpDir := t.TempDir()

	// Route references a vendor that doesn't exist in vendors/
	mustWriteFile(t, tmpDir+"/events/order/routes/order.paid.yaml", `
event_type: "order.paid"
routes:
  - vendor_id: "nonexistent_vendor"
`)

	// No vendors/ directory at all
	// (valid route, no vendors — both warnings but not fatal)

	loader, err := config.NewLoader(tmpDir)
	require.NoError(t, err)

	// Should NOT fail — validation doesn't block startup
	err = loader.Load(context.Background())
	require.NoError(t, err, "Load should not fail when a route references a non-existent vendor")

	// Route should be marked as errored — not available at runtime
	rules, err := loader.GetRoutingRules("order.paid")
	assert.Error(t, err, "route referencing non-existent vendor should be errored")
	assert.Nil(t, rules)
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

// TestConfigLoader_TemplateFieldValidation verifies that the cross-config validation
// logs warnings when delivery contract templates reference fields not in the event schema.
func TestConfigLoader_TemplateFieldValidation(t *testing.T) {
	tmpDir := t.TempDir()

	// Event schema declaring fields: user_id, amount
	mustWriteFile(t, tmpDir+"/events/order/events/order.paid.yaml", `
event_type: "order.paid"
schema:
  type: object
  properties:
    user_id:
      type: string
    amount:
      type: integer
`)

	// Route for order.paid
	mustWriteFile(t, tmpDir+"/events/order/routes/order.paid.yaml", `
event_type: "order.paid"
routes:
  - vendor_id: "test_vendor"
`)

	// Vendor config
	mustWriteFile(t, tmpDir+"/vendors/test_vendor/vendor.yaml", `
vendor_id: "test_vendor"
base_url: "http://example.com/api"
retry_policy:
  max_attempts: 3
  base_delay: 1s
  max_delay: 10s
  multiplier: 2.0
  jitter: 0.2
`)

	// Delivery contract: references user_id (valid) and undefined_field (invalid)
	mustWriteFile(t, tmpDir+"/vendors/test_vendor/order/order.paid.yaml", `
event_type: "order.paid"
request:
  method: POST
  path: "/api/notify"
  headers:
    Content-Type: "application/json"
  body:
    type: mapping
    template:
      user_id: "@{payload:user_id}"
      bad_field: "@{payload:undefined_field}"
`)

	loader, err := config.NewLoader(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, loader)

	// Load should NOT fail — validation doesn't block startup
	err = loader.Load(context.Background())
	require.NoError(t, err, "Load should not fail on template field validation warnings")

	// Contract referencing undeclared field should be errored — not available
	spec, err := loader.GetDeliverySpec("test_vendor", "order.paid")
	assert.Error(t, err, "contract referencing undeclared field should be errored")
	assert.Nil(t, spec)
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	err := os.MkdirAll(filepath.Dir(path), 0755)
	require.NoError(t, err)
	err = os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err)
}
