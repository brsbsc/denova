package handlers

import (
	"testing"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

func TestAllowedTerminalOrigin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"missing origin", "192.168.1.9:7789", "", true},
		{"same-origin http", "192.168.1.9:7789", "http://192.168.1.9:7789", true},
		{"same-origin https", "denova.example", "https://denova.example", true},
		{"vite localhost http", "127.0.0.1:7789", "http://localhost:5173", true},
		{"vite loopback http", "localhost:7789", "http://127.0.0.1:7788", true},
		{"vite localhost https", "127.0.0.1:7789", "https://localhost:5173", true},
		{"vite loopback https", "localhost:7789", "https://127.0.0.1:5173", true},
		{"spoofed port suffix domain localhost", "127.0.0.1:7789", "http://localhost:80.evil.com", false},
		{"spoofed port suffix domain loopback", "127.0.0.1:7789", "http://127.0.0.1:80.evil.com", false},
		{"spoofed subdomain localhost", "127.0.0.1:7789", "http://localhost.evil.com:5173", false},
		{"spoofed userinfo localhost", "127.0.0.1:7789", "http://localhost@evil.com:5173", false},
		{"empty port", "127.0.0.1:7789", "http://localhost:", false},
		{"zero port", "127.0.0.1:7789", "http://localhost:0", false},
		{"out-of-range port", "127.0.0.1:7789", "http://localhost:70000", false},
		{"unexpected path", "127.0.0.1:7789", "http://localhost:5173/path", false},
		{"non-http scheme", "127.0.0.1:7789", "ws://localhost:5173", false},
		{"foreign origin", "192.168.1.9:7789", "http://attacker.example:5173", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := hertzapp.NewContext(0)
			ctx.Request.Header.Set("Host", tc.host)
			if tc.origin != "" {
				ctx.Request.Header.Set("Origin", tc.origin)
			}
			if got := allowedTerminalOrigin(ctx); got != tc.want {
				t.Fatalf("allowedTerminalOrigin(host=%q, origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}
