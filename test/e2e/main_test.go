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
// call StartVendor("unreachable_vendor") explicitly.
func runTests(m *testing.M) int {
	SetupSuite(ExcludeVendors("unreachable_vendor"))
	defer TearDownSuite()

	code := m.Run()

	// Wait for pending deliveries so TearDownSuite doesn't cut them short.
	time.Sleep(500 * time.Millisecond)

	log.Printf("e2e: all tests finished with code %d", code)
	return code
}
