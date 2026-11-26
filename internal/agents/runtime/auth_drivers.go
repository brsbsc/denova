package agentruntime

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"denova/config"
	"denova/internal/hostruntime"
)

const codexDeviceHintKey = "agentRuntime.auth.codexDeviceHint"

type authLaunchSpec struct {
	Mode     string
	Args     []string
	ExtraEnv []string
	UsePTY   bool
	HintKey  string
}

type authPromptUpdate struct {
	AuthURL      string
	UserCode     string
	RedirectPort int
	Ready        bool
}

var (
	ansiCursorForwardRe = regexp.MustCompile(`\x1b\[\d*C`)
	ansiOSCRe           = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiCSIRe           = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiTwoByteRe       = regexp.MustCompile(`\x1b[@-Z\\-_]`)
	httpsURLRe          = regexp.MustCompile(`https://[^\s"'<>()\[\]{}]+`)
	codexDeviceCodeRe   = regexp.MustCompile(`(?:^|[\s:("'\[\]])([A-Z0-9]{4,6}-[A-Z0-9]{4,6})(?:$|[\s.,;:)"'\]])`)
)

func isAuthSupportedEngine(id config.RuntimeID) bool {
	switch id {
	case config.RuntimeCodex, config.RuntimeClaude, config.RuntimeAntigravity:
		return true
	default:
		return false
	}
}

func buildAuthLaunchSpec(id config.RuntimeID, opts AuthStartOptions) (authLaunchSpec, error) {
	method := strings.TrimSpace(opts.Method)
	switch id {
	case config.RuntimeCodex:
		if method != "" {
			return authLaunchSpec{}, ErrAuthInputInvalid
		}
		return authLaunchSpec{
			Mode:     AuthModeDeviceCode,
			Args:     []string{"login", "--device-auth"},
			ExtraEnv: []string{"BROWSER=false", "NO_BROWSER=1"},
			UsePTY:   false,
			HintKey:  codexDeviceHintKey,
		}, nil
	case config.RuntimeAntigravity:
		if method != "" {
			return authLaunchSpec{}, ErrAuthInputInvalid
		}
		return authLaunchSpec{
			Mode:     AuthModeCodePaste,
			Args:     []string{"acp", "--login"},
			ExtraEnv: []string{"SSH_CONNECTION=denova 0 denova 0", "BROWSER=false", "TERM=xterm-256color"},
			UsePTY:   true,
		}, nil
	case config.RuntimeClaude:
		flag := "--claudeai"
		switch method {
		case "", "claudeai":
			flag = "--claudeai"
		case "console":
			flag = "--console"
		default:
			return authLaunchSpec{}, ErrAuthInputInvalid
		}
		return authLaunchSpec{
			Mode:     AuthModeCodePaste,
			Args:     []string{"auth", "login", flag},
			ExtraEnv: []string{"SSH_CONNECTION=denova 0 denova 0", "BROWSER=false", "TERM=xterm-256color"},
			UsePTY:   true,
		}, nil
	default:
		return authLaunchSpec{}, ErrAuthUnsupported
	}
}

func defaultDiscoverAuthCLI(id config.RuntimeID) (string, []string, []string, error) {
	env := os.Environ()
	switch id {
	case config.RuntimeCodex:
		exe := hostruntime.DiscoverCodex(env)
		if exe == "" {
			return "", nil, nil, ErrEngineNotInstalled
		}
		home, err := hostruntime.CodexHome(env)
		if err != nil {
			return "", nil, nil, err
		}
		return exe, nil, []string{"CODEX_HOME=" + home}, nil
	case config.RuntimeClaude:
		launch := hostruntime.DiscoverClaude(env)
		if launch.Executable == "" {
			return "", nil, nil, ErrEngineNotInstalled
		}
		return launch.Executable, launch.Args, nil, nil
	case config.RuntimeAntigravity:
		exe := hostruntime.DiscoverAntigravity(env)
		if exe == "" {
			return "", nil, nil, ErrEngineNotInstalled
		}
		return exe, nil, nil, nil
	default:
		return "", nil, nil, ErrAuthUnsupported
	}
}

func buildAuthProcessEnv(ctx context.Context, layers ...[]string) []string {
	base := hostruntime.WithSystemProxy(ctx, os.Environ())
	type envEntry struct {
		key   string
		value string
	}
	canonicalKey := func(key string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(key)
		}
		// Preserve case-distinct POSIX entries such as HTTP_PROXY and http_proxy.
		return key
	}
	order := make([]string, 0, len(base)+8)
	byKey := make(map[string]envEntry, len(base)+8)

	apply := func(items []string) {
		for _, item := range items {
			key, val, ok := strings.Cut(item, "=")
			if !ok || key == "" {
				continue
			}
			canon := canonicalKey(key)
			if _, exists := byKey[canon]; !exists {
				order = append(order, canon)
			}
			byKey[canon] = envEntry{key: key, value: val}
		}
	}
	apply(base)
	for _, layer := range layers {
		apply(layer)
	}
	result := make([]string, 0, len(order))
	for _, canon := range order {
		entry := byKey[canon]
		result = append(result, entry.key+"="+entry.value)
	}
	return result
}

func stripANSI(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = ansiCursorForwardRe.ReplaceAllString(s, " ")
	s = ansiOSCRe.ReplaceAllString(s, "")
	s = ansiCSIRe.ReplaceAllString(s, "")
	s = ansiTwoByteRe.ReplaceAllString(s, "")

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseAuthPrompt extracts the provider verification URL, optional loopback
// redirect port, and Codex one-time device code from ANSI-stripped CLI output.
// When includeIncompleteTail is false, text after the final line break is held
// back so a URL split across read chunks is never captured truncated.
func parseAuthPrompt(engine config.RuntimeID, cleaned string, includeIncompleteTail bool) authPromptUpdate {
	text := cleaned
	if !includeIncompleteTail {
		idx := strings.LastIndexByte(text, '\n')
		if idx < 0 {
			return authPromptUpdate{}
		}
		text = text[:idx+1]
	}

	var update authPromptUpdate
	urls := httpsURLRe.FindAllString(text, -1)
	for _, candidate := range urls {
		trimmed := strings.TrimRight(candidate, ".,;:!?)\"]>'")
		parsed, err := url.Parse(trimmed)
		if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
			continue
		}
		if matchesEngineAuthURL(engine, parsed) {
			update.AuthURL = trimmed
			break
		}
	}

	switch engine {
	case config.RuntimeCodex:
		withoutURLs := httpsURLRe.ReplaceAllString(text, " ")
		if matches := codexDeviceCodeRe.FindAllStringSubmatch(withoutURLs, -1); len(matches) > 0 {
			update.UserCode = matches[len(matches)-1][1]
		}
		update.Ready = update.AuthURL != "" && update.UserCode != ""
	case config.RuntimeAntigravity, config.RuntimeClaude:
		if update.AuthURL != "" {
			update.RedirectPort = extractRedirectLoopbackPort(update.AuthURL)
			update.Ready = true
		}
	}
	return update
}

func matchesEngineAuthURL(engine config.RuntimeID, parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	h := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if h == "" {
		return false
	}
	switch engine {
	case config.RuntimeCodex:
		return h == "auth.openai.com" || h == "chatgpt.com"
	case config.RuntimeAntigravity:
		return h == "accounts.google.com"
	case config.RuntimeClaude:
		return h == "claude.ai" || h == "console.anthropic.com" || h == "platform.claude.com"
	default:
		return false
	}
}

func extractRedirectLoopbackPort(authURL string) int {
	parsed, err := url.Parse(authURL)
	if err != nil {
		return 0
	}
	redirectRaw := strings.TrimSpace(parsed.Query().Get("redirect_uri"))
	if redirectRaw == "" {
		return 0
	}
	redirectParsed, err := url.Parse(redirectRaw)
	if err != nil || !strings.EqualFold(redirectParsed.Scheme, "http") || redirectParsed.User != nil {
		return 0
	}
	host := strings.ToLower(redirectParsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" {
		return 0
	}
	portStr := redirectParsed.Port()
	if portStr == "" {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

var antigravityProtectedNames = map[string]bool{
	"settings.json":   true,
	"hooks.json":      true,
	"mcp_config.json": true,
	"config.json":     true,
	"installation_id": true,
	"gemini.md":       true,
}

var antigravityCredentialNames = map[string]bool{
	"oauth_creds.json":       true,
	"oauth_token.json":       true,
	"oauth_tokens.json":      true,
	"google_accounts.json":   true,
	"credentials.json":       true,
	"credentials.enc":        true,
	"auth.json":              true,
	"auth_token.json":        true,
	"auth_tokens.json":       true,
	"auth_state.json":        true,
	"token.json":             true,
	"tokens.json":            true,
	"agy_auth.json":          true,
	"antigravity_auth.json":  true,
	"antigravity_oauth.json": true,
}

func isAntigravityCredentialFile(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || antigravityProtectedNames[lower] {
		return false
	}
	if antigravityCredentialNames[lower] {
		return true
	}
	return strings.Contains(lower, "oauth") || strings.Contains(lower, "token") || strings.Contains(lower, "credential")
}

// clearAntigravityCredentials removes Antigravity CLI OAuth credential files
// directly inside ~/.gemini and ~/.gemini/antigravity-cli without touching
// user settings, conversations, hooks, or other subdirectories.
func clearAntigravityCredentials(homeDir string) error {
	if strings.TrimSpace(homeDir) == "" {
		return errors.New("resolve user home directory")
	}
	targetDirs := []string{
		filepath.Join(homeDir, ".gemini"),
		filepath.Join(homeDir, ".gemini", "antigravity-cli"),
	}
	var errs []error
	for _, dir := range targetDirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !isAntigravityCredentialFile(entry.Name()) {
				continue
			}
			fullPath := filepath.Join(dir, entry.Name())
			info, statErr := os.Lstat(fullPath)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				errs = append(errs, statErr)
				continue
			}
			// Never follow symlinks or remove directories.
			if !info.Mode().IsRegular() {
				continue
			}
			if rmErr := os.Remove(fullPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				errs = append(errs, rmErr)
			}
		}
	}
	return errors.Join(errs...)
}
