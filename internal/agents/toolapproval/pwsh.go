package toolapproval

import (
	"regexp"
	"strings"
	"unicode"

	"denova/config"
)

var powerShellProviderPath = regexp.MustCompile(`(?i)^[a-z][a-z0-9.+-]*:`)

func classifyPowerShell(command, workspace, cwd string, mode config.AgentApprovalMode) Decision {
	return classifyPowerShellWithAllowedFiles(command, workspace, cwd, mode, nil)
}

func classifyPowerShellWithAllowedFiles(command, workspace, cwd string, mode config.AgentApprovalMode, allowedFiles []string) Decision {
	boundary, ok := newPathBoundaryWithAllowedFiles(workspace, cwd, allowedFiles)
	if !ok {
		return prompt("invalid_workspace_boundary", RiskHigh,
			"无法验证命令工作目录，需要你的确认。 / The command working directory cannot be verified, so approval is required.")
	}
	if strings.ContainsAny(command, "$`{}()><\r\n") || strings.Contains(command, "&&") || strings.Contains(command, "||") {
		return prompt("pwsh_dynamic_syntax", RiskHigh,
			"PowerShell 命令包含动态表达式、重定向或复杂语法，需要你的确认。 / The PowerShell command uses dynamic expressions, redirects, or complex syntax and requires approval.")
	}
	segments := strings.Split(command, "|")
	highest := commandRead
	for _, segment := range segments {
		words, ok := splitSimplePowerShellWords(segment)
		if !ok || len(words) == 0 {
			return prompt("pwsh_parse_failed", RiskHigh,
				"无法可靠解析 PowerShell 命令，需要你的确认。 / The PowerShell command could not be parsed reliably, so approval is required.")
		}
		class := classifyPowerShellCall(words, boundary, mode)
		if class == commandUnknown {
			return prompt("pwsh_unlisted_command", RiskHigh,
				"命令不在当前模式的自动允许列表中，需要你的确认。 / The command is not in this mode's automatic allowlist and requires approval.")
		}
		if class > highest {
			highest = class
		}
	}
	if highest == commandRead {
		return allow("pwsh_safe_read", RiskLow,
			"命令仅包含白名单内的工作区读取操作。 / The command contains only allowlisted workspace read operations.")
	}
	if highest == commandNetworkRead {
		return allow("pwsh_network_read", RiskMedium,
			"Write 模式允许该网络读取或下载命令。 / Write mode allows this network read or download command.")
	}
	return allow("pwsh_workspace_write", RiskMedium,
		"Write 模式允许该工作区开发或写入命令。 / Write mode allows this workspace development or write command.")
}

func splitSimplePowerShellWords(input string) ([]string, bool) {
	var words []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, char := range strings.TrimSpace(input) {
		if escaped {
			current.WriteRune(char)
			escaped = false
			continue
		}
		if char == '`' {
			return nil, false
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == ';' || char == '&' || char == '\r' || char == '\n' {
			return nil, false
		}
		if unicode.IsSpace(char) {
			flush()
			continue
		}
		current.WriteRune(char)
	}
	if escaped || quote != 0 {
		return nil, false
	}
	flush()
	return words, true
}

func classifyPowerShellCall(words []string, boundary pathBoundary, mode config.AgentApprovalMode) commandClass {
	name := strings.ToLower(words[0])
	args := words[1:]
	for _, arg := range args {
		if arg == "--%" {
			return commandUnknown
		}
	}
	switch name {
	case "get-location", "pwd", "get-childitem", "gci", "ls", "dir":
		if powerShellPathsInside(args, boundary) {
			return commandRead
		}
	case "get-content", "gc", "cat", "type", "measure-object", "select-string", "compare-object", "get-item", "get-command":
		if powerShellPathsInside(args, boundary) {
			return commandRead
		}
	case "git":
		if safeGitRead(args, boundary) {
			return commandRead
		}
	}
	if mode != config.AgentApprovalWrite {
		return commandUnknown
	}
	// Attached files extend only the read boundary. They are immutable inputs,
	// not extra workspace paths that Write mode may mutate automatically.
	writeBoundary := boundary
	writeBoundary.allowedFiles = nil
	switch name {
	case "new-item", "set-content", "add-content", "copy-item", "move-item", "remove-item":
		paths, ok := powerShellPathArguments(args)
		if !ok {
			return commandUnknown
		}
		if name == "remove-item" && (len(paths) == 0 || containsBroadWorkspaceTarget(writeBoundary, paths)) {
			return commandUnknown
		}
		if allPathsInside(writeBoundary, paths) {
			return commandWrite
		}
	case "git":
		return classifyGitWrite(args, writeBoundary)
	case "npm", "pnpm", "yarn", "bun", "npx", "bunx":
		if safePackageCommand(name, args, writeBoundary) {
			return commandWrite
		}
	case "go", "cargo", "dotnet", "msbuild":
		if safeDevelopmentCommand(name, args, writeBoundary) || (name == "dotnet" || name == "msbuild") &&
			!hasExternalLiteralArgument(args, writeBoundary) && !containsArgument(args, "publish", "deploy") {
			return commandWrite
		}
	case "invoke-webrequest", "iwr":
		if powerShellNetworkRead(args, writeBoundary) {
			return commandNetworkRead
		}
	}
	return commandUnknown
}

func powerShellPathsInside(args []string, boundary pathBoundary) bool {
	paths, ok := powerShellPathArguments(args)
	if !ok {
		return false
	}
	for _, value := range paths {
		value = strings.TrimSpace(value)
		if powerShellProviderPath.MatchString(value) &&
			!(len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/')) {
			return false
		}
	}
	return allPathsInside(boundary, paths)
}

func splitPowerShellParameter(arg string) (string, string, bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
		return "", "", false
	}
	flag, inlineValue, hasInline := strings.Cut(arg, ":")
	return strings.ToLower(flag), inlineValue, hasInline
}

func matchesPowerShellFlagPrefix(flag string, minLen int, full string) bool {
	return len(flag) >= minLen && len(flag) <= len(full) && strings.HasPrefix(full, flag)
}

func powerShellPathArguments(args []string) ([]string, bool) {
	paths := make([]string, 0)
	flagsEnded := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !flagsEnded && arg == "--" {
			flagsEnded = true
			continue
		}
		if !flagsEnded && strings.HasPrefix(arg, "-") && arg != "-" {
			flag, inlineValue, hasInline := splitPowerShellParameter(arg)
			if powerShellValueParameter(flag) {
				if hasInline {
					if strings.TrimSpace(inlineValue) == "" {
						return nil, false
					}
					paths = append(paths, inlineValue)
					continue
				}
				if index+1 < len(args) && !strings.HasPrefix(args[index+1], "-") {
					index++
					paths = append(paths, args[index])
					continue
				}
				// -File and -F may act as boolean switches (e.g., Get-ChildItem -File, Remove-Item -Force).
				if matchesPowerShellFlagPrefix(flag, 2, "-file") {
					continue
				}
				return nil, false
			}
			if hasInline {
				trimmed := strings.TrimSpace(inlineValue)
				if looksLikeExternalPath(trimmed) || powerShellProviderPath.MatchString(trimmed) {
					paths = append(paths, trimmed)
				}
			}
			continue
		}
		paths = append(paths, arg)
	}
	return paths, true
}

func powerShellValueParameter(flag string) bool {
	return matchesPowerShellFlagPrefix(flag, 2, "-path") ||
		matchesPowerShellFlagPrefix(flag, 2, "-literalpath") ||
		flag == "-lp" ||
		matchesPowerShellFlagPrefix(flag, 2, "-pspath") ||
		matchesPowerShellFlagPrefix(flag, 2, "-destination") ||
		matchesPowerShellFlagPrefix(flag, 2, "-filepath") ||
		matchesPowerShellFlagPrefix(flag, 2, "-outfile") ||
		matchesPowerShellFlagPrefix(flag, 4, "-referenceobject") ||
		matchesPowerShellFlagPrefix(flag, 4, "-differenceobject")
}

func powerShellDeletePathParameter(flag string) bool {
	return matchesPowerShellFlagPrefix(flag, 2, "-path") ||
		matchesPowerShellFlagPrefix(flag, 2, "-literalpath") ||
		flag == "-lp" ||
		matchesPowerShellFlagPrefix(flag, 2, "-pspath")
}

func powerShellNetworkRead(args []string, boundary pathBoundary) bool {
	uri := ""
	consumeValue := func(index *int, inlineValue string, hasInline bool) (string, bool) {
		if hasInline {
			value := strings.TrimSpace(inlineValue)
			return value, value != ""
		}
		if *index+1 >= len(args) {
			return "", false
		}
		*index++
		value := strings.TrimSpace(args[*index])
		return value, value != ""
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if uri == "" {
				uri = arg
			}
			continue
		}
		flag, inlineValue, hasInline := splitPowerShellParameter(arg)
		if flag == "" {
			return false
		}
		switch {
		case matchesPowerShellFlagPrefix(flag, 3, "-uri"):
			value, ok := consumeValue(&index, inlineValue, hasInline)
			if !ok {
				return false
			}
			uri = value
		case matchesPowerShellFlagPrefix(flag, 2, "-method"):
			method, ok := consumeValue(&index, inlineValue, hasInline)
			if !ok || !strings.EqualFold(method, "get") {
				return false
			}
		case matchesPowerShellFlagPrefix(flag, 2, "-outfile"):
			outFile, ok := consumeValue(&index, inlineValue, hasInline)
			if !ok || !boundary.containsLiteral(outFile) {
				return false
			}
		case matchesPowerShellFlagPrefix(flag, 5, "-usebasicparsing"):
			if hasInline {
				trimmed := strings.TrimPrefix(strings.TrimSpace(inlineValue), "$")
				if !strings.EqualFold(trimmed, "true") && !strings.EqualFold(trimmed, "false") && trimmed != "1" && trimmed != "0" {
					return false
				}
			}
		case matchesPowerShellFlagPrefix(flag, 4, "-timeoutsec"),
			matchesPowerShellFlagPrefix(flag, 4, "-maximumredirection"),
			matchesPowerShellFlagPrefix(flag, 6, "-useragent"):
			if _, ok := consumeValue(&index, inlineValue, hasInline); !ok {
				return false
			}
		default:
			// Reject abbreviated or unrecognized flags (-Me, -InF, -Body, -Form, -Token,
			// -UseDefaultCredentials, -Headers, etc.) so network writes/exfiltration prompt.
			return false
		}
	}
	lowerURI := strings.ToLower(strings.TrimSpace(uri))
	return strings.HasPrefix(lowerURI, "https://") || strings.HasPrefix(lowerURI, "http://")
}
