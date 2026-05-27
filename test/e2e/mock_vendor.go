package e2e

import (
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// MockResponse defines a single response the mock vendor should return.
type MockResponse struct {
	StatusCode int
	Body       string
	Delay      time.Duration // delay before responding (for graceful shutdown tests)
}

// VendorRequest records a request received by the mock vendor.
type VendorRequest struct {
	Method  string
	Path    string
	Headers map[string][]string
	Body    []byte
}

// MockVendor simulates a single external vendor API for E2E tests.
// Each MockVendor instance handles one vendor endpoint on its own address.
// Tests control behavior directly via Go method calls.
type MockVendor struct {
	mu        sync.Mutex
	server    *http.Server
	addr      string
	responses []MockResponse
	requests  []VendorRequest
	callCount int
	signals   []chan struct{}
}

// NewMockVendor creates a new MockVendor with default 200 OK response.
func NewMockVendor() *MockVendor {
	return &MockVendor{}
}

// Start starts the mock vendor HTTP server on the given address (e.g. ":19091").
func (m *MockVendor) Start(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", m.handleRequest)

	m.addr = lis.Addr().String()
	m.server = &http.Server{Handler: mux}

	go func() {
		if err := m.server.Serve(lis); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("mock vendor server error")
		}
	}()

	return nil
}

// Addr returns the address the server is listening on.
func (m *MockVendor) Addr() string {
	return m.addr
}

// BaseURL returns the base URL for this vendor (e.g. "http://127.0.0.1:19091").
func (m *MockVendor) BaseURL() string {
	return "http://" + m.addr
}

// Close shuts down the mock vendor HTTP server.
func (m *MockVendor) Close() {
	if m.server != nil {
		m.server.Close()
	}
}

// RegisterBehavior sets the response sequence for this vendor.
// Each call consumes one response. If more calls are made than registered
// responses, the last response is reused.
func (m *MockVendor) RegisterBehavior(responses []MockResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = responses
	m.callCount = 0
}

// WaitRequest blocks until a request arrives for this vendor
// or until the timeout expires. Returns nil on timeout.
func (m *MockVendor) WaitRequest(timeout time.Duration) *VendorRequest {
	sig := make(chan struct{}, 1)

	m.mu.Lock()
	if len(m.requests) > 0 {
		req := m.requests[len(m.requests)-1]
		m.mu.Unlock()
		return &req
	}
	m.signals = append(m.signals, sig)
	m.mu.Unlock()

	select {
	case <-sig:
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.requests) > 0 {
			return &m.requests[len(m.requests)-1]
		}
		return nil
	case <-time.After(timeout):
		return nil
	}
}

// Requests returns all requests received by this vendor.
func (m *MockVendor) Requests() []VendorRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]VendorRequest, len(m.requests))
	copy(result, m.requests)
	return result
}

// LatestRequest returns the most recent request, or nil if none.
func (m *MockVendor) LatestRequest() *VendorRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return nil
	}
	return &m.requests[len(m.requests)-1]
}

func (m *MockVendor) handleRequest(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()

	vr := VendorRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: r.Header.Clone(),
		Body:    body,
	}

	var resp MockResponse

	m.mu.Lock()
	m.requests = append(m.requests, vr)

	if m.callCount < len(m.responses) {
		resp = m.responses[m.callCount]
	} else if len(m.responses) > 0 {
		resp = m.responses[len(m.responses)-1]
	} else {
		resp = MockResponse{StatusCode: 200, Body: "ok"}
	}
	m.callCount++

	// Signal all waiters
	for _, sig := range m.signals {
		select {
		case sig <- struct{}{}:
		default:
		}
	}
	m.signals = nil
	m.mu.Unlock()

	// Simulate delay before responding (for graceful shutdown tests)
	if resp.Delay > 0 {
		time.Sleep(resp.Delay)
	}

	w.WriteHeader(resp.StatusCode)
	w.Write([]byte(resp.Body))
}
