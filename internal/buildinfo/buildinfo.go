package buildinfo

import (
	"strconv"
	"strings"
)

// Version is injected by release builds. The default keeps source/dev runs from
// treating themselves as installable release binaries.
var Version = "dev"

// Distribution is fixed by packaging. Desktop program files belong to the
// signed desktop application and must never be replaced by the Web updater.
var Distribution = "web"

// DevelopmentVersion is the public capability target of this source tree.
const DevelopmentVersion = "0.6.1"

// Repository is the GitHub repository whose releases update this build.
// Release builds of a fork inject their own repository, so the updater never
// replaces a fork build with an upstream package that lacks its changes.
var Repository = "alfredxw/denova"

// BaseReleaseVersion normalizes a release version for semantic-version host
// capability checks. Fork hotfix versions (X.Y.Zfix or X.Y.ZfixN) target the
// public capability contract of their base X.Y.Z release; non-hotfix strings
// return with surrounding whitespace and an optional leading "v" trimmed so
// callers' semver parsers validate or reject them normally.
func BaseReleaseVersion(v string) string {
	normalized := strings.TrimPrefix(strings.TrimSpace(v), "v")
	release, number, hasFix := strings.Cut(normalized, "fix")
	if !hasFix {
		return normalized
	}
	parts := strings.Split(release, ".")
	if len(parts) != 3 {
		return normalized
	}
	for _, part := range parts {
		if !isCanonicalDigits(part) {
			return normalized
		}
		if _, err := strconv.Atoi(part); err != nil {
			return normalized
		}
	}
	if number != "" {
		if !isCanonicalDigits(number) {
			return normalized
		}
		if n, err := strconv.Atoi(number); err != nil || n <= 0 {
			return normalized
		}
	}
	return release
}

func isCanonicalDigits(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
