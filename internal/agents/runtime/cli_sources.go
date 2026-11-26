package agentruntime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"denova/config"
	"denova/internal/agents/runtime/external/antigravity"
	"denova/internal/agents/runtime/external/claude"
	"denova/internal/agents/runtime/external/codex"
	"github.com/Masterminds/semver/v3"
)

const (
	defaultAntigravityBaseURL = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app"
	defaultClaudeBaseURL      = "https://downloads.claude.ai/claude-code-releases"
	defaultCodexBaseURL       = "https://registry.npmjs.org/@openai/codex"

	defaultMaxDownloadBytes int64 = 1 << 30 // 1 GiB
	defaultMaxExtractBytes  int64 = 1 << 30 // 1 GiB
	maxManifestBytes        int64 = 4 << 20 // 4 MiB
	maxTarOverheadBytes     int64 = 16 << 20
	maxArchiveEntries             = 30000

	cliManifestTimeout = 30 * time.Second
	cliDownloadTimeout = 30 * time.Minute
	cliVerifyTimeout   = 15 * time.Second

	cliArchiveNone  = ""
	cliArchiveTarGz = "tar.gz"
)

var managedCLIEngines = []config.RuntimeID{
	config.RuntimeAntigravity,
	config.RuntimeClaude,
	config.RuntimeCodex,
}

// CLIRelease describes one verified Linux release artifact resolved from an
// official vendor manifest before downloading.
type CLIRelease struct {
	Version      string
	DownloadURL  string
	HashAlgo     string // "sha256" or "sha512"
	Digest       []byte // decoded raw digest bytes
	ExpectedSize int64  // >0 when manifest specifies exact byte length
	Archive      string // "" for direct binary, "tar.gz" for gzipped tar archive
}

// CLISource resolves the latest official Linux release metadata for a managed CLI.
type CLISource interface {
	Resolve(ctx context.Context, client *http.Client, goarch string) (CLIRelease, error)
}

func isManagedCLIEngine(id config.RuntimeID) bool {
	switch id {
	case config.RuntimeAntigravity, config.RuntimeClaude, config.RuntimeCodex:
		return true
	default:
		return false
	}
}

func cliBinaryName(id config.RuntimeID) string {
	switch id {
	case config.RuntimeAntigravity:
		return "agy"
	case config.RuntimeClaude:
		return "claude"
	case config.RuntimeCodex:
		return "codex"
	default:
		return ""
	}
}

func cliMinimumVersion(id config.RuntimeID) (*semver.Version, error) {
	switch id {
	case config.RuntimeAntigravity:
		return semver.MustParse(antigravity.MinimumWindowsVersion), nil
	case config.RuntimeClaude:
		return semver.MustParse(claude.MinimumVersion), nil
	case config.RuntimeCodex:
		return semver.MustParse(codex.MinimumVersion), nil
	default:
		return nil, ErrCLIManagementUnsupported
	}
}

func defaultCLISources(baseOverrides map[config.RuntimeID]string) map[config.RuntimeID]CLISource {
	baseFor := func(id config.RuntimeID, fallback string) string {
		if raw := strings.TrimSpace(baseOverrides[id]); raw != "" {
			return strings.TrimRight(raw, "/")
		}
		return fallback
	}
	return map[config.RuntimeID]CLISource{
		config.RuntimeAntigravity: antigravityCLISource{baseURL: baseFor(config.RuntimeAntigravity, defaultAntigravityBaseURL)},
		config.RuntimeClaude:      claudeCLISource{baseURL: baseFor(config.RuntimeClaude, defaultClaudeBaseURL)},
		config.RuntimeCodex:       codexCLISource{baseURL: baseFor(config.RuntimeCodex, defaultCodexBaseURL)},
	}
}

type antigravityCLISource struct {
	baseURL string
}

func (s antigravityCLISource) Resolve(ctx context.Context, client *http.Client, goarch string) (CLIRelease, error) {
	if goarch != "amd64" && goarch != "arm64" {
		return CLIRelease{}, fmt.Errorf("unsupported Linux architecture %q for Antigravity CLI", goarch)
	}
	manifestURL := fmt.Sprintf("%s/manifests/linux_%s.json", strings.TrimRight(s.baseURL, "/"), goarch)
	body, err := fetchHTTPSMetadata(ctx, client, manifestURL, "application/json")
	if err != nil {
		return CLIRelease{}, fmt.Errorf("fetch Antigravity manifest: %w", err)
	}
	var manifest struct {
		Version string `json:"version"`
		URL     string `json:"url"`
		SHA512  string `json:"sha512"`
		Size    int64  `json:"size"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return CLIRelease{}, fmt.Errorf("decode Antigravity manifest: %w", err)
	}
	version, err := validateReleaseVersion(config.RuntimeAntigravity, manifest.Version)
	if err != nil {
		return CLIRelease{}, err
	}
	downloadURL := strings.TrimSpace(manifest.URL)
	if err := validateHTTPSURL(downloadURL); err != nil {
		return CLIRelease{}, fmt.Errorf("invalid Antigravity download URL: %w", err)
	}
	digest, err := decodeHexDigest(manifest.SHA512, sha512.Size)
	if err != nil {
		return CLIRelease{}, fmt.Errorf("invalid Antigravity SHA-512 checksum: %w", err)
	}
	if manifest.Size < 0 {
		return CLIRelease{}, errors.New("invalid Antigravity manifest size")
	}
	archive := cliArchiveNone
	if parsed, err := url.Parse(downloadURL); err == nil {
		lowerPath := strings.ToLower(parsed.Path)
		if strings.HasSuffix(lowerPath, ".tar.gz") || strings.HasSuffix(lowerPath, ".tgz") {
			archive = cliArchiveTarGz
		}
	}
	return CLIRelease{
		Version:      version,
		DownloadURL:  downloadURL,
		HashAlgo:     "sha512",
		Digest:       digest,
		ExpectedSize: manifest.Size,
		Archive:      archive,
	}, nil
}

type claudeCLISource struct {
	baseURL string
}

func (s claudeCLISource) Resolve(ctx context.Context, client *http.Client, goarch string) (CLIRelease, error) {
	platformKey, err := linuxPlatformKey(goarch)
	if err != nil {
		return CLIRelease{}, fmt.Errorf("unsupported Linux architecture %q for Claude Code", goarch)
	}
	base := strings.TrimRight(s.baseURL, "/")
	latestBody, err := fetchHTTPSMetadata(ctx, client, base+"/latest", "text/plain")
	if err != nil {
		return CLIRelease{}, fmt.Errorf("fetch Claude Code latest version: %w", err)
	}
	version, err := validateReleaseVersion(config.RuntimeClaude, string(latestBody))
	if err != nil {
		return CLIRelease{}, err
	}
	manifestURL := fmt.Sprintf("%s/%s/manifest.json", base, url.PathEscape(version))
	manifestBody, err := fetchHTTPSMetadata(ctx, client, manifestURL, "application/json")
	if err != nil {
		return CLIRelease{}, fmt.Errorf("fetch Claude Code manifest: %w", err)
	}
	var manifest struct {
		Version   string `json:"version"`
		Platforms map[string]struct {
			Checksum string `json:"checksum"`
			Size     int64  `json:"size"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		return CLIRelease{}, fmt.Errorf("decode Claude Code manifest: %w", err)
	}
	if strings.TrimSpace(manifest.Version) != "" {
		manifestVer, err := normalizeSemver(manifest.Version)
		if err != nil || manifestVer != version {
			return CLIRelease{}, fmt.Errorf("Claude Code manifest version mismatch")
		}
	}
	platform, ok := manifest.Platforms[platformKey]
	if !ok {
		return CLIRelease{}, fmt.Errorf("Claude Code manifest missing platform %q", platformKey)
	}
	digest, err := decodeHexDigest(platform.Checksum, sha256.Size)
	if err != nil {
		return CLIRelease{}, fmt.Errorf("invalid Claude Code SHA-256 checksum: %w", err)
	}
	if platform.Size <= 0 {
		return CLIRelease{}, errors.New("Claude Code manifest missing valid binary size")
	}
	downloadURL := fmt.Sprintf("%s/%s/%s/claude", base, url.PathEscape(version), platformKey)
	if err := validateHTTPSURL(downloadURL); err != nil {
		return CLIRelease{}, fmt.Errorf("invalid Claude Code download URL: %w", err)
	}
	return CLIRelease{
		Version:      version,
		DownloadURL:  downloadURL,
		HashAlgo:     "sha256",
		Digest:       digest,
		ExpectedSize: platform.Size,
		Archive:      cliArchiveNone,
	}, nil
}

type codexCLISource struct {
	baseURL string
}

func (s codexCLISource) Resolve(ctx context.Context, client *http.Client, goarch string) (CLIRelease, error) {
	platformKey, err := linuxPlatformKey(goarch)
	if err != nil {
		return CLIRelease{}, fmt.Errorf("unsupported Linux architecture %q for Codex CLI", goarch)
	}
	base := strings.TrimRight(s.baseURL, "/")
	latestBody, err := fetchHTTPSMetadata(ctx, client, base+"/latest", "application/json")
	if err != nil {
		return CLIRelease{}, fmt.Errorf("fetch Codex latest metadata: %w", err)
	}
	var latest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(latestBody, &latest); err != nil {
		return CLIRelease{}, fmt.Errorf("decode Codex latest metadata: %w", err)
	}
	version, err := validateReleaseVersion(config.RuntimeCodex, latest.Version)
	if err != nil {
		return CLIRelease{}, err
	}
	pkgURL := fmt.Sprintf("%s/%s-%s", base, url.PathEscape(version), platformKey)
	pkgBody, err := fetchHTTPSMetadata(ctx, client, pkgURL, "application/json")
	if err != nil {
		return CLIRelease{}, fmt.Errorf("fetch Codex platform package metadata: %w", err)
	}
	var pkg struct {
		Dist struct {
			Tarball   string `json:"tarball"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(pkgBody, &pkg); err != nil {
		return CLIRelease{}, fmt.Errorf("decode Codex platform package metadata: %w", err)
	}
	tarballURL := strings.TrimSpace(pkg.Dist.Tarball)
	if err := validateHTTPSURL(tarballURL); err != nil {
		return CLIRelease{}, fmt.Errorf("invalid Codex tarball URL: %w", err)
	}
	digest, err := parseSHA512SRI(pkg.Dist.Integrity)
	if err != nil {
		return CLIRelease{}, fmt.Errorf("invalid Codex SRI integrity: %w", err)
	}
	return CLIRelease{
		Version:     version,
		DownloadURL: tarballURL,
		HashAlgo:    "sha512",
		Digest:      digest,
		Archive:     cliArchiveTarGz,
	}, nil
}

func linuxPlatformKey(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "linux-x64", nil
	case "arm64":
		return "linux-arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q", goarch)
	}
}

func validateHTTPSURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" {
		return errors.New("URL must use https with a non-empty host")
	}
	return nil
}

func fetchHTTPSMetadata(ctx context.Context, client *http.Client, rawURL, accept string) ([]byte, error) {
	if err := validateHTTPSURL(rawURL); err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, cliManifestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "denova-cli-manager")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxManifestBytes {
		return nil, errors.New("metadata response exceeds size limit")
	}
	return data, nil
}

func decodeHexDigest(raw string, expectedLen int) ([]byte, error) {
	cleaned := strings.TrimSpace(raw)
	digest, err := hex.DecodeString(cleaned)
	if err != nil || len(digest) != expectedLen {
		return nil, fmt.Errorf("expected %d-byte hex digest", expectedLen)
	}
	return digest, nil
}

func parseSHA512SRI(integrity string) ([]byte, error) {
	for _, token := range strings.Fields(integrity) {
		encoded, ok := strings.CutPrefix(token, "sha512-")
		if !ok || encoded == "" {
			continue
		}
		digest, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(digest) != sha512.Size {
			return nil, errors.New("malformed sha512 SRI digest")
		}
		return digest, nil
	}
	return nil, errors.New("missing sha512 SRI integrity")
}

func normalizeSemver(raw string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	v, err := semver.StrictNewVersion(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid semantic version %q", trimmed)
	}
	return v.String(), nil
}

func validateReleaseVersion(id config.RuntimeID, raw string) (string, error) {
	minVer, err := cliMinimumVersion(id)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	v, err := semver.StrictNewVersion(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid %s version %q", id, trimmed)
	}
	if v.LessThan(minVer) {
		return "", fmt.Errorf("%s version %s is below minimum required %s", id, v.String(), minVer.String())
	}
	return v.String(), nil
}

func computeUpdateAvailable(installed bool, installedVersion, latestVersion string) bool {
	if !installed || strings.TrimSpace(latestVersion) == "" {
		return false
	}
	latest, err2 := semver.StrictNewVersion(strings.TrimPrefix(strings.TrimSpace(latestVersion), "v"))
	if err2 != nil {
		return false
	}
	if strings.TrimSpace(installedVersion) == "" {
		// Installed binary has an unknown or incompatible version; allow updating to latest.
		return true
	}
	inst, err1 := semver.StrictNewVersion(strings.TrimPrefix(strings.TrimSpace(installedVersion), "v"))
	if err1 != nil {
		return true
	}
	return latest.GreaterThan(inst)
}

func sameSemver(a, b string) bool {
	na, err1 := normalizeSemver(a)
	nb, err2 := normalizeSemver(b)
	if err1 != nil || err2 != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return na == nb
}

func defaultVerifyBinary(ctx context.Context, id config.RuntimeID, binaryPath string) (string, error) {
	verifyCtx, cancel := context.WithTimeout(ctx, cliVerifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(verifyCtx, binaryPath, "--version")
	cmd.Stderr = io.Discard
	cmd.WaitDelay = cliVerifyTimeout
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("verify %s binary --version: %w", id, err)
	}
	return parseVerifiedCLIVersion(id, string(out))
}

// parseVerifiedCLIVersion validates `--version` stdout according to each CLI
// adapter's version format and enforces the minimum container-safe version.
func parseVerifiedCLIVersion(id config.RuntimeID, output string) (string, error) {
	minVer, err := cliMinimumVersion(id)
	if err != nil {
		return "", err
	}
	var raw string
	switch id {
	case config.RuntimeAntigravity:
		raw = strings.TrimSpace(output)
	case config.RuntimeClaude:
		fields := strings.Fields(output)
		if len(fields) == 0 {
			return "", errors.New("empty Claude Code version output")
		}
		raw = fields[0]
	case config.RuntimeCodex:
		value, ok := strings.CutPrefix(strings.TrimSpace(output), "codex-cli ")
		if !ok {
			return "", errors.New("unrecognized Codex CLI version output")
		}
		raw = strings.TrimPrefix(strings.TrimSpace(value), "v")
	default:
		return "", ErrCLIManagementUnsupported
	}
	v, err := semver.StrictNewVersion(raw)
	if err != nil {
		return "", fmt.Errorf("invalid %s version output: %w", id, err)
	}
	if v.LessThan(minVer) {
		return "", fmt.Errorf("%s version %s is below minimum required %s", id, v.String(), minVer.String())
	}
	return v.String(), nil
}

func archiveBinaryMatcher(id config.RuntimeID, goarch string) func(cleanPath string) int {
	switch id {
	case config.RuntimeAntigravity:
		return func(cleanPath string) int {
			switch {
			case cleanPath == "antigravity":
				return 3
			case path.Base(cleanPath) == "antigravity":
				return 2
			case cleanPath == "agy":
				return 2
			case path.Base(cleanPath) == "agy":
				return 1
			default:
				return 0
			}
		}
	case config.RuntimeCodex:
		expectedTriple := "x86_64-unknown-linux-musl"
		if goarch == "arm64" {
			expectedTriple = "aarch64-unknown-linux-musl"
		}
		return func(cleanPath string) int {
			parts := strings.Split(cleanPath, "/")
			if len(parts) > 0 && parts[0] == "package" {
				parts = parts[1:]
			}
			if len(parts) != 4 || parts[0] != "vendor" || parts[1] == "" || parts[3] != "codex" {
				return 0
			}
			if parts[2] != "codex" && parts[2] != "bin" {
				return 0
			}
			if parts[1] == expectedTriple {
				return 2
			}
			return 1
		}
	default:
		return func(string) int { return 0 }
	}
}

type boundedStreamReader struct {
	r         io.Reader
	remaining int64
}

func (b *boundedStreamReader) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		var probe [1]byte
		n, err := b.r.Read(probe[:])
		if n > 0 {
			return 0, errors.New("archive decompressed stream exceeds extraction size limit")
		}
		if err != nil {
			return 0, err
		}
		return 0, errors.New("archive decompressed stream exceeds extraction size limit")
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.r.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// extractSingleBinaryFromTarGz validates every entry in a .tar.gz archive
// against path traversal, symlinks/hardlinks, special files, and extraction
// size caps before writing the matched binary to destPath.
func extractSingleBinaryFromTarGz(archivePath, destPath string, match func(cleanPath string) int, maxExtractBytes int64) error {
	if maxExtractBytes <= 0 {
		maxExtractBytes = defaultMaxExtractBytes
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar.gz archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read gzip archive: %w", err)
	}
	defer gz.Close()

	streamCap := maxExtractBytes + maxTarOverheadBytes
	if streamCap < maxExtractBytes {
		streamCap = maxExtractBytes
	}
	tr := tar.NewReader(&boundedStreamReader{r: gz, remaining: streamCap})

	var (
		entries        int
		totalExpanded  int64
		bestPriority   int
		matched        bool
		extractedBytes int64
	)

	cleanup := func() {
		_ = os.Remove(destPath)
	}

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			return fmt.Errorf("read tar entry: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			cleanup()
			return errors.New("archive exceeds maximum entry count limit")
		}

		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader:
			continue
		case tar.TypeDir:
			if _, err := validateArchivePath(hdr.Name, true); err != nil {
				cleanup()
				return err
			}
			continue
		case tar.TypeReg, tar.TypeRegA:
		case tar.TypeSymlink, tar.TypeLink:
			cleanup()
			return fmt.Errorf("archive contains forbidden link entry %q", hdr.Name)
		default:
			cleanup()
			return fmt.Errorf("archive contains unsupported special entry %q", hdr.Name)
		}

		cleanName, err := validateArchivePath(hdr.Name, false)
		if err != nil {
			cleanup()
			return err
		}
		if hdr.Size < 0 || hdr.Size > maxExtractBytes-totalExpanded {
			cleanup()
			return errors.New("archive exceeds extraction size limit")
		}
		totalExpanded += hdr.Size

		priority := match(cleanName)
		if priority <= bestPriority {
			continue
		}
		written, err := copyArchiveEntryToFile(destPath, tr, hdr.Size)
		if err != nil {
			cleanup()
			return err
		}
		if written == 0 {
			cleanup()
			return errors.New("matched binary entry in archive is empty")
		}
		bestPriority = priority
		matched = true
		extractedBytes = written
	}

	if err := gz.Close(); err != nil {
		cleanup()
		return fmt.Errorf("verify gzip archive trailer: %w", err)
	}
	if !matched || extractedBytes == 0 {
		cleanup()
		return errors.New("archive does not contain expected CLI binary")
	}
	return nil
}

func validateArchivePath(name string, allowDot bool) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, `\`) || strings.Contains(name, ":") {
		return "", fmt.Errorf("archive contains invalid path %q", name)
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("archive contains absolute path %q", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." {
			return "", fmt.Errorf("archive contains path traversal %q", name)
		}
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("archive contains path traversal %q", name)
	}
	if clean == "." && !allowDot {
		return "", fmt.Errorf("archive contains invalid file path %q", name)
	}
	return clean, nil
}

func copyArchiveEntryToFile(destPath string, r io.Reader, expectedSize int64) (int64, error) {
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open staged binary file: %w", err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(r, expectedSize+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return 0, fmt.Errorf("extract binary entry: %w", copyErr)
	}
	if written != expectedSize {
		return 0, errors.New("extracted binary size does not match archive header")
	}
	if syncErr != nil {
		return 0, fmt.Errorf("sync extracted binary: %w", syncErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("close extracted binary: %w", closeErr)
	}
	return written, nil
}
