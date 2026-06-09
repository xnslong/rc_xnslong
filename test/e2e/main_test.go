package e2e

import (
	"log"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests is a wrapper so TearDownSuite can run via defer before os.Exit.
// unreachable_vendor is excluded because its test requires the port to
// have no listener (simulating network failure). Tests that need it can
// call StartVendor("unreachable_vendor_19999") explicitly.
//
// IncludeVendors declares vendors whose config is broken (unparseable YAML,
// missing files, invalid retry) so their mocks still start. This reflects
// the real-world invariant that a vendor's API server runs regardless of
// the notification system's config state — the test framework must be able
// to observe whether the system sends HTTP requests to them.
func runTests(m *testing.M) int {
	SetupSuite(
		ExcludeVendors("unreachable_vendor_19999"),
		IncludeVendors(
			VendorSpec{ID: "bad_vendor_18001", Port: 18001},
			VendorSpec{ID: "invalid_retry_vendor_18002", Port: 18002},
			VendorSpec{ID: "missing_yaml_vendor_18003", Port: 18003},
		),
	)
	defer TearDownSuite()

	code := m.Run()

	// Wait for pending deliveries so TearDownSuite doesn't cut them short.
	time.Sleep(500 * time.Millisecond)

	log.Printf("e2e: all tests finished with code %d", code)
	return code
}
