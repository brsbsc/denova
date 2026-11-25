package desktop

import "testing"

func TestDesktopReleaseURLUsesRepositoryWithFallback(t *testing.T) {
	for _, tc := range []struct {
		repo    string
		version string
		want    string
	}{
		{"alfredxw/denova", "0.8.0", "https://github.com/alfredxw/denova/releases/tag/v0.8.0"},
		{"brsbsc/denova", "0.8.0fix1", "https://github.com/brsbsc/denova/releases/tag/v0.8.0fix1"},
		{"brsbsc/denova", "v0.8.0fix1", "https://github.com/brsbsc/denova/releases/tag/v0.8.0fix1"},
		{"", "0.8.0", "https://github.com/alfredxw/denova/releases/tag/v0.8.0"},
	} {
		if got := desktopReleaseURL(tc.repo, tc.version); got != tc.want {
			t.Fatalf("desktopReleaseURL(%q, %q) = %q, want %q", tc.repo, tc.version, got, tc.want)
		}
	}
}
