package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"denova/config"
	"denova/internal/publicnet"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func tinyPNGBytes(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 1, 1))
	canvas.Set(0, 0, color.RGBA{R: 12, G: 34, B: 56, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNewServiceAndAdaptersHaveNonZeroTimeouts(t *testing.T) {
	svc := NewService()
	for _, protocol := range []string{
		config.ImageProtocolOpenAI,
		config.ImageProtocolXAI,
		config.ImageProtocolArk,
		config.ImageProtocolGemini,
		config.ImageProtocolComfyUI,
	} {
		if svc.adapters[protocol] == nil {
			t.Fatalf("missing adapter for protocol %q", protocol)
		}
	}
	if NewOpenAIAdapter(nil).httpClient.Timeout <= 0 {
		t.Fatal("OpenAIAdapter default timeout must be positive")
	}
	if NewXAIAdapter(nil).httpClient.Timeout <= 0 {
		t.Fatal("XAIAdapter default timeout must be positive")
	}
	if NewArkAdapter(nil).httpClient.Timeout <= 0 {
		t.Fatal("ArkAdapter default timeout must be positive")
	}
	if NewGeminiAdapter(nil).httpClient.Timeout <= 0 {
		t.Fatal("GeminiAdapter default timeout must be positive")
	}
	if NewComfyUIAdapter(nil).httpClient.Timeout <= 0 {
		t.Fatal("ComfyUIAdapter default timeout must be positive")
	}
	if newPublicImageDownloadClient(0).Timeout <= 0 {
		t.Fatal("public image download client default timeout must be positive")
	}
}

func TestRemoteAdaptersBlockSSRFImageDownloadTargets(t *testing.T) {
	pngData := tinyPNGBytes(t)
	var secretHits atomic.Int32
	secretServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secretHits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngData)
	}))
	t.Cleanup(secretServer.Close)

	targets := []struct {
		name       string
		url        string
		wantPolicy bool
	}{
		{"loopback httptest server", secretServer.URL + "/secret.png", true},
		{"localhost hostname", "http://localhost:8080/secret.png", true},
		{"ipv6 loopback", "http://[::1]:8080/secret.png", true},
		{"private rfc1918", "http://10.0.0.1/secret.png", true},
		{"cloud metadata link-local", "http://169.254.169.254/latest/meta-data/", true},
		{"non-http scheme", "file:///etc/passwd", false},
	}

	for _, tc := range targets {
		t.Run("openai/"+tc.name, func(t *testing.T) {
			providerClient := &http.Client{
				Timeout: time.Second,
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Host != "api.openai.com" {
						return nil, errors.New("unexpected provider host: " + req.URL.Host)
					}
					body, _ := json.Marshal(imagesAPIResponse{Data: []imagesAPIItem{{URL: tc.url}}})
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(bytes.NewReader(body)),
						Request:    req,
					}, nil
				}),
			}
			adapter := NewOpenAIAdapter(providerClient)
			_, err := adapter.Generate(context.Background(), config.ResolvedImageAPIProfile{
				ProfileID: "remote",
				Provider:  config.ImageProviderOpenAI,
				Protocol:  config.ImageProtocolOpenAI,
				BaseURL:   "https://api.openai.com/v1",
				Model:     "gpt-image-1",
			}, GenerateRequest{Prompt: "skyline", N: 1})
			if err == nil {
				t.Fatalf("expected SSRF target %q to fail", tc.url)
			}
			if tc.wantPolicy && !strings.Contains(err.Error(), "blocked address") {
				t.Fatalf("expected policy block for %q, got %v", tc.url, err)
			}
		})
	}

	arkClient := &http.Client{
		Timeout: time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := json.Marshal(imagesAPIResponse{Data: []imagesAPIItem{{URL: secretServer.URL + "/secret.png"}}})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    req,
			}, nil
		}),
	}
	if _, err := NewArkAdapter(arkClient).Generate(context.Background(), config.ResolvedImageAPIProfile{
		ProfileID: "ark",
		Provider:  "ark",
		Protocol:  config.ImageProtocolArk,
		BaseURL:   "https://ark.cn-beijing.volces.com/api/v3",
		Model:     "seedream",
	}, GenerateRequest{Prompt: "skyline", N: 1}); err == nil {
		t.Fatal("expected ArkAdapter SSRF image URL to fail")
	}

	if hits := secretHits.Load(); hits != 0 {
		t.Fatalf("local secret server received %d requests, want 0", hits)
	}
}

func TestPublicImageDownloadBlocksPrivateRedirectsAndStripsCrossHostAuth(t *testing.T) {
	pngData := tinyPNGBytes(t)
	var secretHits atomic.Int32
	secretServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secretHits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngData)
	}))
	t.Cleanup(secretServer.Close)

	for _, redirectURL := range []string{
		secretServer.URL + "/secret.png",
		"http://localhost:8080/secret.png",
		"http://169.254.169.254/latest/meta-data/",
		"http://192.168.1.10/internal.png",
		"http://[::1]:8080/secret.png",
		"file:///etc/passwd",
	} {
		client := newPublicImageDownloadClient(time.Second)
		var hops atomic.Int32
		client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if hops.Add(1) > 1 {
				return nil, errors.New("redirect target should have been rejected before RoundTrip")
			}
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{redirectURL}},
				Body:       http.NoBody,
				Request:    req,
			}, nil
		})
		_, _, err := downloadImageURL(context.Background(), client, "https://93.184.216.34/start.png")
		if err == nil {
			t.Fatalf("redirect to %q unexpectedly succeeded", redirectURL)
		}
		if strings.HasPrefix(redirectURL, "http://") && !publicnet.IsPolicyError(err) {
			t.Fatalf("redirect to %q err = %v, want publicnet policy error", redirectURL, err)
		}
		if hops.Load() != 1 {
			t.Fatalf("redirect to %q executed %d hops, want 1", redirectURL, hops.Load())
		}
	}
	if secretHits.Load() != 0 {
		t.Fatalf("secret server hit %d times during redirect checks", secretHits.Load())
	}

	client := newPublicImageDownloadClient(time.Second)
	var secondHopAuth string
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "93.184.216.34" {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://93.184.216.35/final.png"}},
				Body:       http.NoBody,
				Request:    req,
			}, nil
		}
		secondHopAuth = req.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(bytes.NewReader(pngData)),
			Request:    req,
		}, nil
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://93.184.216.34/start.png", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("public redirect failed: %v", err)
	}
	_ = resp.Body.Close()
	if secondHopAuth != "" {
		t.Fatalf("Authorization leaked across origins: %q", secondHopAuth)
	}
}

func TestLocalProviderDownloadsAndClientTimeout(t *testing.T) {
	pngData := tinyPNGBytes(t)
	var localServer *httptest.Server
	localServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/images/generations":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(imagesAPIResponse{
				Data: []imagesAPIItem{{URL: localServer.URL + "/generated/1.png"}},
			})
		case "/generated/1.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngData)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(localServer.Close)

	result, err := NewOpenAIAdapter(nil).Generate(context.Background(), config.ResolvedImageAPIProfile{
		ProfileID: "local",
		Provider:  config.ImageProviderOpenAI,
		Protocol:  config.ImageProtocolOpenAI,
		BaseURL:   localServer.URL + "/v1",
		Model:     "local-sd",
	}, GenerateRequest{Prompt: "lantern", N: 1})
	if err != nil || len(result.Images) != 1 || !bytes.Equal(result.Images[0].Data, pngData) {
		t.Fatalf("local provider generate = %+v, err = %v", result, err)
	}

	unblock := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-unblock:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(unblock)
		stalled.Close()
	})

	fastTimeoutAdapter := NewOpenAIAdapter(&http.Client{Timeout: 40 * time.Millisecond})
	start := time.Now()
	_, err = fastTimeoutAdapter.Generate(context.Background(), config.ResolvedImageAPIProfile{
		ProfileID: "stalled",
		Provider:  config.ImageProviderOpenAI,
		Protocol:  config.ImageProtocolOpenAI,
		BaseURL:   stalled.URL + "/v1",
		Model:     "gpt-image-1",
	}, GenerateRequest{Prompt: "lantern", N: 1})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("expected fast client timeout, got err=%v elapsed=%v", err, time.Since(start))
	}
}
