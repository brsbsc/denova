package activity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"denova/internal/messages"
	"denova/internal/update"
)

var ErrUpdateNotesRange = errors.New("invalid update notes version range")

// UpdateNotes is a reading projection, independent of download/apply state.
// An empty FromVersion selects the published history through ToVersion.
type UpdateNotes struct {
	FromVersion string                  `json:"from_version,omitempty"`
	ToVersion   string                  `json:"to_version"`
	Releases    []messages.ReleaseNotes `json:"releases"`
	Complete    bool                    `json:"complete"`
}

func (service *Service) UpdateNotes(ctx context.Context, fromVersion, toVersion, locale string) (UpdateNotes, error) {
	target, ok := update.ParseReleaseVersion(toVersion)
	if !ok {
		return UpdateNotes{}, ErrUpdateNotesRange
	}
	from := ""
	if fromVersion != "" {
		from, ok = update.ParseReleaseVersion(fromVersion)
		if !ok || update.CompareVersions(from, target) >= 0 {
			return UpdateNotes{}, ErrUpdateNotesRange
		}
	}
	var content string
	status, err := service.updates.Status()
	if err != nil {
		return UpdateNotes{}, err
	}
	if strings.TrimPrefix(status.CurrentVersion, "v") == target {
		content, err = messages.NewService(service.dataDir).ReadChangelog()
	} else {
		content, err = service.updates.Changelog(ctx, target)
	}
	if err != nil {
		return UpdateNotes{}, err
	}
	if len(content) > update.MaxChangelogBytes {
		return UpdateNotes{}, fmt.Errorf("changelog exceeds %d bytes", update.MaxChangelogBytes)
	}
	result := UpdateNotes{FromVersion: fromVersion, ToVersion: toVersion, Releases: []messages.ReleaseNotes{}}
	seenFrom, seenTarget := from == "", false
	for _, release := range messages.ParseReleaseNotes(content, locale) {
		version, ok := update.ParseReleaseVersion(release.Version)
		if !ok {
			continue
		}
		if from != "" && update.CompareVersions(version, from) == 0 {
			seenFrom = true
		}
		if update.CompareVersions(version, target) == 0 {
			seenTarget = true
		}
		if update.CompareVersions(version, target) > 0 || (from != "" && update.CompareVersions(version, from) <= 0) {
			continue
		}
		result.Releases = append(result.Releases, release)
	}
	sort.SliceStable(result.Releases, func(i, j int) bool {
		return update.CompareVersions(result.Releases[i].Version, result.Releases[j].Version) > 0
	})
	result.Complete = seenFrom && seenTarget
	slog.InfoContext(ctx, "update_notes_loaded", "from_version", fromVersion, "to_version", toVersion, "releases", len(result.Releases), "complete", result.Complete)
	return result, nil
}

func (service *Service) upgradeMessages(history []messages.Message) []messages.Message {
	completed, err := service.updates.CompletedUpdate()
	if err != nil {
		slog.Warn("completed_update_notice_unavailable", "error", err)
		return history
	}
	if completed == nil {
		return history
	}
	// One upgrade is one notification. The complete version history remains
	// available in UpdateNotes, without turning old releases into new unread mail.
	return []messages.Message{{
		ID: "update:" + completed.ID, Type: messages.MessageTypeUpdate,
		Title: completed.Version, Version: completed.Version, PreviousVersion: completed.PreviousVersion,
		PublishedAt: completed.CompletedAt.Format(time.RFC3339),
	}}
}
