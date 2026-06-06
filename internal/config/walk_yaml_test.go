package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Test helpers ----

// simpleVendor is a minimal vendor YAML struct for testing.
type simpleVendor struct {
	VendorID string `yaml:"vendor_id"`
	BaseURL  string `yaml:"base_url"`
}

// simpleContract is a minimal delivery contract YAML struct for testing.
type simpleContract struct {
	EventType string `yaml:"event_type"`
	Request   struct {
		Method string `yaml:"method"`
	} `yaml:"request"`
}

// brokenType has fields that won't match a standard vendor.yaml.
type brokenType struct {
	Name string `yaml:"name"`
}

// ---- walkYAML — successful cases ----

func TestWalkYAML_VendorConfig(t *testing.T) {
	rootDir := "testdata/walkyaml"

	var got []simpleVendor
	walkYAML[simpleVendor](rootDir, "vendors/{vendor}/vendor.yaml",
		func(v simpleVendor, _ string, _ map[string]string, err error) {
			if err == nil {
				got = append(got, v)
			}
		})

	// good_vendor/vendor.yaml parses successfully. bad_vendor has broken YAML.
	require.Len(t, got, 1, "only good_vendor should parse successfully")
	assert.Equal(t, "good_vendor", got[0].VendorID)
}

func TestWalkYAML_DeliveryContract(t *testing.T) {
	rootDir := "testdata/walkyaml"

	var got []simpleContract
	walkYAML[simpleContract](rootDir, "vendors/{vendor}/{biz}/{event}.yaml",
		func(c simpleContract, _ string, _ map[string]string, err error) {
			if err == nil {
				got = append(got, c)
			}
		})

	require.Len(t, got, 1, "only order.paid contract should exist")
	assert.Equal(t, "order.paid", got[0].EventType)
}

// ---- walkYAML — variable capture ----

func TestWalkYAML_Variables(t *testing.T) {
	rootDir := "testdata/walkyaml"

	var varsList []map[string]string
	walkYAML[simpleVendor](rootDir, "vendors/{vendor}/vendor.yaml",
		func(_ simpleVendor, _ string, vars map[string]string, err error) {
			if err == nil {
				varsList = append(varsList, vars)
			}
		})

	require.Len(t, varsList, 1, "only good_vendor succeeds")
	v := varsList[0]
	assert.Equal(t, "good_vendor", v["vendor"])
}

func TestWalkYAML_ContractVariables(t *testing.T) {
	rootDir := "testdata/walkyaml"

	walkYAML[simpleContract](rootDir, "vendors/{vendor}/{biz}/{event}.yaml",
		func(c simpleContract, _ string, vars map[string]string, err error) {
			if err != nil {
				return
			}
			assert.Equal(t, "good_vendor", vars["vendor"])
			assert.Equal(t, "order", vars["biz"])
			assert.Equal(t, "order.paid", vars["event"])
		})
}

// ---- walkYAML — parse error cases ----

func TestWalkYAML_ParseError(t *testing.T) {
	rootDir := "testdata/walkyaml"

	var parseErrors int
	walkYAML[brokenType](rootDir, "vendors/{vendor}/vendor.yaml",
		func(_ brokenType, _ string, _ map[string]string, err error) {
			if err != nil {
				parseErrors++
				assert.Contains(t, err.Error(), "parsing YAML")
			}
		})

	// bad_vendor/vendor.yaml has broken YAML → 1 error.
	// good_vendor/vendor.yaml is valid YAML but has no "name" field → unmarshal
	// silently zero-fills (yaml.Unmarshal doesn't error on missing fields).
	assert.Equal(t, 1, parseErrors, "only the broken YAML file should produce a parse error")
}

func TestWalkYAML_ReadError(t *testing.T) {
	// Create a temporary config tree with an unreadable file.
	dir := t.TempDir()

	// Set up: vendors/v/0.yaml (readable), vendors/v/1.yaml (unreadable)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "vendors", "v"), 0755))
	mustWriteFile(t, filepath.Join(dir, "vendors", "v", "0.yaml"), "x: 1")
	unreadable := filepath.Join(dir, "vendors", "v", "1.yaml")
	mustWriteFile(t, unreadable, "x: 2")
	require.NoError(t, os.Chmod(unreadable, 0000))
	t.Cleanup(func() { os.Chmod(unreadable, 0644) }) // allow cleanup

	var readableCalls, readErrors int
	walkYAML[simpleVendor](dir, "vendors/{vendor}/{file}.yaml",
		func(_ simpleVendor, path string, _ map[string]string, err error) {
			if err != nil {
				readErrors++
				assert.Contains(t, err.Error(), "reading file")
				return
			}
			readableCalls++
		})

	assert.Equal(t, 1, readableCalls, "the readable file should succeed")
	assert.Equal(t, 1, readErrors, "the unreadable file should produce a read error")
}

// ---- walkYAML — missing/empty directory ----

func TestWalkYAML_MissingDir(t *testing.T) {
	var calls int
	walkYAML[simpleVendor]("/nonexistent/path", "vendors/{vendor}/vendor.yaml",
		func(_ simpleVendor, _ string, _ map[string]string, _ error) {
			calls++
		})

	assert.Equal(t, 0, calls, "no files should be found when root is missing")
}

func TestWalkYAML_EmptyDir(t *testing.T) {
	rootDir := "testdata/walkyaml"

	var calls int
	walkYAML[simpleVendor](rootDir, "vendors/{vendor}/nonexistent_dir/{file}.yaml",
		func(_ simpleVendor, _ string, _ map[string]string, _ error) {
			calls++
		})

	// {vendor} iterates good_vendor & bad_vendor, but neither has a
	// nonexistent_dir subdirectory → the literal segment stops the walk.
	assert.Equal(t, 0, calls, "no files should match a non-existent literal subdirectory")
}

// ---- errSentinel — callback can distinguish success vs failure ----

func TestWalkYAML_ErrInCallback(t *testing.T) {
	rootDir := "testdata/walkyaml"
	var successCount, failCount int

	walkYAML[simpleVendor](rootDir, "vendors/{vendor}/vendor.yaml",
		func(_ simpleVendor, _ string, _ map[string]string, err error) {
			if err != nil {
				failCount++
			} else {
				successCount++
			}
		})

	// good_vendor/vendor.yaml is valid → 1 success
	// bad_vendor/vendor.yaml has broken YAML → 1 failure
	assert.Equal(t, 1, successCount)
	assert.Equal(t, 1, failCount)
}

// ---- bad YAML file ----

func TestWalkYAML_BadYamlFile(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir+"/test.yaml", "{{invalid: yaml: broken\n  - no")

	var callErr error
	walkYAML[simpleVendor](dir, "test.yaml",
		func(_ simpleVendor, _ string, _ map[string]string, err error) {
			callErr = err
		})

	require.Error(t, callErr)
	assert.Contains(t, callErr.Error(), "parsing YAML")
}

// ---- multiple files ----

func TestWalkYAML_MultipleFiles(t *testing.T) {
	rootDir := "testdata"

	var visited []string
	walkYAML[simpleVendor](rootDir, "{dir}/vendor.yaml",
		func(_ simpleVendor, path string, _ map[string]string, _ error) {
			visited = append(visited, path)
		})

	// Should find vendor.yamls in testdata subdirectories
	require.NotEmpty(t, visited, "at least one vendor.yaml should be found")
	t.Logf("visited %d files: %v", len(visited), visited)
}

// ---- helpers ----

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := writeFile(path, []byte(content)); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
