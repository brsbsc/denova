package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"

	"denova/internal/publicnet"
)

const (
	maxImageAPIJSONBytes        = 64 << 20
	defaultImageHTTPTimeout     = 5 * time.Minute
	defaultImageDownloadTimeout = 2 * time.Minute
)

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultImageHTTPTimeout}
}

func isLocalEndpointURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.TrimSpace(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsUnspecified()
}

func imageDownloadClient(baseClient *http.Client, providerBaseURL string) *http.Client {
	if isLocalEndpointURL(providerBaseURL) {
		if baseClient != nil {
			return baseClient
		}
		return defaultHTTPClient()
	}
	var timeout time.Duration
	if baseClient != nil && baseClient.Timeout > 0 {
		timeout = baseClient.Timeout
	}
	return newPublicImageDownloadClient(timeout)
}

func newPublicImageDownloadClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultImageDownloadTimeout
	}
	client := publicnet.NewHTTPClient()
	client.Timeout = timeout
	if transport, ok := client.Transport.(*http.Transport); ok {
		transport.TLSHandshakeTimeout = 15 * time.Second
		transport.ResponseHeaderTimeout = 30 * time.Second
	}
	client.CheckRedirect = checkPublicImageRedirect
	return client
}

func checkPublicImageRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if err := validatePublicImageTarget(req.Context(), req.URL); err != nil {
		return err
	}
	if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		req.Header.Del("Authorization")
	}
	return nil
}

func validatePublicImageTarget(ctx context.Context, parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return errors.New("invalid image download URL")
	}
	return publicnet.ValidateHost(ctx, parsed.Hostname())
}

type imageAPIHTTPError struct {
	status  int
	message string
}

func (err *imageAPIHTTPError) Error() string {
	return fmt.Sprintf("image API returned HTTP %d: %s", err.status, err.message)
}

func doJSON(ctx context.Context, client *http.Client, method, endpoint string, headers map[string]string, body, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode image API request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("create image API request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("send image API request: %w", err)
	}
	defer response.Body.Close()
	payload, err := readLimited(response.Body, maxImageAPIJSONBytes)
	if err != nil {
		return fmt.Errorf("read image API response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return imageAPIStatusError(response.StatusCode, payload)
	}
	if target == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode image API response: %w", err)
	}
	return nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func imageAPIStatusError(status int, payload []byte) error {
	var envelope struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	message := strings.TrimSpace(string(payload))
	if json.Unmarshal(payload, &envelope) == nil {
		switch value := envelope.Error.(type) {
		case string:
			message = value
		case map[string]any:
			if text, ok := value["message"].(string); ok {
				message = text
			}
		}
		if message == "" {
			message = envelope.Message
		}
	}
	if len(message) > 1000 {
		message = message[:1000] + "..."
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &imageAPIHTTPError{status: status, message: message}
}

func bearerHeaders(apiKey string, extra map[string]string) map[string]string {
	headers := make(map[string]string, len(extra)+1)
	if strings.TrimSpace(apiKey) != "" {
		headers["Authorization"] = "Bearer " + strings.TrimSpace(apiKey)
	}
	for key, value := range extra {
		headers[key] = value
	}
	return headers
}

func endpointURL(baseURL, suffix string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid image API base URL %q", baseURL)
	}
	parsed.Path = path.Join(strings.TrimSuffix(parsed.Path, "/"), suffix)
	return parsed.String(), nil
}
