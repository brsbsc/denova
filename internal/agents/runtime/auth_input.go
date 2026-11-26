package agentruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (m *AuthManager) replayLoopbackCallback(ctx context.Context, session *authSession, replayURL string) error {
	reqCtx, cancel := context.WithTimeout(ctx, authCallbackTimeout)
	defer cancel()

	stopSessionCancel := context.AfterFunc(session.ctx, cancel)
	defer stopSessionCancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, replayURL, nil)
	if err != nil {
		// Never wrap url errors because they contain the callback URL and OAuth code.
		return errors.New("failed to build local authentication callback request")
	}
	resp, err := m.callbackClient.Do(req)
	if err != nil {
		return errors.New("failed to deliver local authentication callback")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()
	// Local CLI callback servers commonly reply with 200 OK or a 3xx redirect
	// to a completion page (redirects are intentionally not followed).
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return errors.New("local authentication callback rejected")
	}
	return nil
}

// validateAuthInput enforces strict control-character rejection and loopback
// SSRF protection before any code is written to a PTY or any HTTP request is
// issued. Any URL-like input is strictly validated against the session's
// advertised loopback redirect_uri port and never falls through to PTY stdin.
func validateAuthInput(raw string, expectedRedirectPort int) (normalized string, replayURL string, isCallback bool, err error) {
	if len(raw) > maxAuthInputBytes {
		return "", "", false, ErrAuthInputInvalid
	}
	for _, r := range raw {
		if r == '\r' || r == '\n' || r == 0 || r == 0x7f || (r < 0x20 && r != '\t') {
			return "", "", false, ErrAuthInputInvalid
		}
	}
	trimmed := strings.Trim(raw, " \t")
	if trimmed == "" || len(trimmed) > maxAuthInputBytes {
		return "", "", false, ErrAuthInputInvalid
	}
	if looksLikeURL(trimmed) {
		parsed, parseErr := url.Parse(trimmed)
		if parseErr != nil || !strings.EqualFold(parsed.Scheme, "http") || parsed.Opaque != "" || parsed.User != nil {
			return "", "", false, ErrAuthInputInvalid
		}
		host := strings.ToLower(parsed.Hostname())
		if host != "localhost" && host != "127.0.0.1" {
			return "", "", false, ErrAuthInputInvalid
		}
		if expectedRedirectPort <= 0 {
			return "", "", false, ErrAuthInputInvalid
		}
		portStr := parsed.Port()
		port, convErr := strconv.Atoi(portStr)
		if convErr != nil || port != expectedRedirectPort {
			return "", "", false, ErrAuthInputInvalid
		}
		path := parsed.Path
		if path == "" {
			path = "/"
		}
		rebuilt := &url.URL{
			Scheme:   "http",
			Host:     fmt.Sprintf("127.0.0.1:%d", expectedRedirectPort),
			Path:     path,
			RawPath:  parsed.RawPath,
			RawQuery: parsed.RawQuery,
		}
		return trimmed, rebuilt.String(), true, nil
	}
	// Pure authorization codes must not contain embedded tabs or whitespace injection.
	if strings.ContainsRune(trimmed, '\t') {
		return "", "", false, ErrAuthInputInvalid
	}
	return trimmed, "", false, nil
}

func looksLikeURL(value string) bool {
	if strings.Contains(value, "://") || strings.HasPrefix(value, "//") {
		return true
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{"http:", "https:", "file:", "ftp:", "ws:", "wss:", "gopher:"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func generateAuthSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
