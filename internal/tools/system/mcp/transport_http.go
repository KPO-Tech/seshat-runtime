package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// httpAcceptHeader is what the Streamable HTTP transport requires of every client: a server may answer either way,
	// and some refuse a request that does not accept both.
	httpAcceptHeader = "application/json, text/event-stream"
	// httpSessionHeader carries the session a server gave at initialization, which it expects on every later request.
	httpSessionHeader = "Mcp-Session-Id"
	// httpErrorBodyLimit bounds what a refusal adds to its error.
	httpErrorBodyLimit = 1 << 10
)

// HTTPTransport implements the Streamable HTTP transport for MCP
type HTTPTransport struct {
	// config is the transport configuration
	config HTTPTransportConfig

	// sessionID is the session the server gave, empty when it gave none
	sessionID   string
	sessionIDMu sync.RWMutex

	// httpClient is the HTTP client
	httpClient *http.Client

	// responseChan channels responses by request ID
	responseChan map[int64]chan *JSONRPCResponse

	// responseChanMu protects responseChan
	responseChanMu sync.RWMutex

	// notificationsHandler handles incoming notifications
	notificationsHandler NotificationHandler

	// methodsHandler handles incoming method calls
	methodsHandler MethodHandler

	// started indicates if the transport has been started
	started bool

	// closed indicates if the transport is closed
	closed bool
}

// rejectMCPHTTPURL validates the URL for an MCP HTTP transport.
// It blocks cloud metadata endpoints and credential-bearing URLs while
// intentionally allowing localhost/private addresses (local MCP servers are valid).
func rejectMCPHTTPURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid MCP HTTP URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("MCP HTTP URL scheme must be http or https, got %q", scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("MCP HTTP URL must not contain embedded credentials")
	}
	// Block the most dangerous SSRF targets: cloud instance-metadata endpoints.
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "169.254.169.254", // AWS/GCP/Azure IMDSv1
		"fd00:ec2::254",            // AWS IMDSv6
		"metadata.google.internal", // GCP metadata
		"metadata.goog",            // GCP metadata alias
		"169.254.170.2":            // ECS task metadata
		return fmt.Errorf("MCP HTTP URL targets a cloud metadata endpoint, which is not allowed")
	}
	return nil
}

// NewHTTPTransport creates a new HTTP transport
func NewHTTPTransport(config HTTPTransportConfig) (*HTTPTransport, error) {
	if err := rejectMCPHTTPURL(config.URL); err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPTransport{
		config: config,
		httpClient: &http.Client{
			Timeout: timeout,
			// Re-validate every redirect target through the same SSRF guard used
			// for the initial URL. Without this, a malicious server could issue a
			// 302 to http://169.254.169.254/ and bypass rejectMCPHTTPURL.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return rejectMCPHTTPURL(req.URL.String())
			},
		},
		responseChan: make(map[int64]chan *JSONRPCResponse),
	}, nil
}

// Start starts the HTTP transport
func (t *HTTPTransport) Start(ctx context.Context) error {
	if t.started {
		return fmt.Errorf("transport already started")
	}

	t.started = true
	return nil
}

// Send sends a JSON-RPC request via HTTP
func (t *HTTPTransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	if !t.started {
		return nil, fmt.Errorf("transport not started")
	}

	if t.closed {
		return nil, fmt.Errorf("transport closed")
	}

	// Marshal request
	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.config.URL, bytes.NewReader(reqData))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	t.setHeaders(httpReq)

	// Send request
	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer httpResp.Body.Close()

	// Check status code
	if httpResp.StatusCode != http.StatusOK {
		return nil, httpStatusError(httpResp)
	}
	t.rememberSession(httpResp)

	mediaType, _, _ := mime.ParseMediaType(httpResp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return readEventStreamResponse(httpResp.Body, req.ID)
	}

	// Parse response
	var resp JSONRPCResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &resp, nil
}

// setHeaders sets what every request carries. The configured headers come last, so that a server needing another value can have it.
func (t *HTTPTransport) setHeaders(httpReq *http.Request) {
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", httpAcceptHeader)
	t.sessionIDMu.RLock()
	sessionID := t.sessionID
	t.sessionIDMu.RUnlock()
	if sessionID != "" {
		httpReq.Header.Set(httpSessionHeader, sessionID)
	}
	for k, v := range t.config.Headers {
		httpReq.Header.Set(k, v)
	}
}

func (t *HTTPTransport) rememberSession(httpResp *http.Response) {
	sessionID := strings.TrimSpace(httpResp.Header.Get(httpSessionHeader))
	if sessionID == "" {
		return
	}
	t.sessionIDMu.Lock()
	t.sessionID = sessionID
	t.sessionIDMu.Unlock()
}

// httpStatusError names the status and what the server said about it: a server's refusal usually says what it wants.
func httpStatusError(httpResp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(httpResp.Body, httpErrorBodyLimit))
	detail := strings.TrimSpace(string(body))
	var refusal JSONRPCResponse
	if err := json.Unmarshal(body, &refusal); err == nil && refusal.Error != nil {
		detail = refusal.Error.Message
	}
	if detail == "" {
		return fmt.Errorf("HTTP error: %s", httpResp.Status)
	}
	return fmt.Errorf("HTTP error: %s: %s", httpResp.Status, detail)
}

// readEventStreamResponse reads a server-sent event stream up to the response to the request with this ID. The stream
// may carry notifications and requests from the server first.
func readEventStreamResponse(body io.Reader, id int64) (*JSONRPCResponse, error) {
	want := []byte(strconv.FormatInt(id, 10))
	reader := bufio.NewReader(body)
	var data []string
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if resp := eventResponse(data, want); resp != nil {
				return resp, nil
			}
			data = data[:0]
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if readErr == io.EOF {
			if resp := eventResponse(data, want); resp != nil {
				return resp, nil
			}
			return nil, fmt.Errorf("event stream ended without a response")
		}
		if readErr != nil {
			return nil, fmt.Errorf("failed to read event stream: %w", readErr)
		}
	}
}

// eventResponse gives the response an event holds when it answers the request with this ID, and nil otherwise.
func eventResponse(data []string, want []byte) *JSONRPCResponse {
	if len(data) == 0 {
		return nil
	}
	payload := []byte(strings.Join(data, "\n"))
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(payload, &message); err != nil || message.Method != "" || !bytes.Equal(message.ID, want) {
		return nil
	}
	var resp JSONRPCResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil
	}
	return &resp
}

// SendNotification sends a JSON-RPC notification via HTTP
func (t *HTTPTransport) SendNotification(ctx context.Context, notification *JSONRPCNotification) error {
	if !t.started {
		return fmt.Errorf("transport not started")
	}

	if t.closed {
		return fmt.Errorf("transport closed")
	}

	// Marshal notification
	reqData, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.config.URL, bytes.NewReader(reqData))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	t.setHeaders(httpReq)

	// Send request (don't wait for the answer to a notification), but read what is sent back and close the body: left open,
	// the connection is never given back to the pool.
	resp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.Body.Close()
}

// Close closes the HTTP transport
func (t *HTTPTransport) Close() error {
	if t.closed {
		return nil
	}

	t.closed = true
	t.httpClient.CloseIdleConnections()
	return nil
}

// SetNotificationsHandler sets the handler for incoming notifications
func (t *HTTPTransport) SetNotificationsHandler(handler NotificationHandler) {
	t.notificationsHandler = handler
}

// SetMethodsHandler sets the handler for incoming method calls
func (t *HTTPTransport) SetMethodsHandler(handler MethodHandler) {
	t.methodsHandler = handler
}
