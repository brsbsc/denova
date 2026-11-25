package platform

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"denova/internal/interactive"
	"denova/internal/platform"
	"denova/internal/portablepath"
	"github.com/google/uuid"
)

const platformStoryBindingOwner = "denova.platform"

type platformStoryBinding struct {
	Instance platform.Instance `json:"instance"`
	Removed  bool              `json:"removed,omitempty"`
}

func (h Stories) Instances(ctx context.Context) ([]platform.Instance, error) {
	projects, err := h.host.Projects()
	if err != nil {
		return nil, err
	}
	items := []platform.Instance{}
	for _, project := range projects {
		store, err := h.host.OpenStoryStore(ctx, project.ID)
		if err != nil {
			slog.WarnContext(ctx, "platform_story_store_unavailable", "project", project.ID, "error", err)
			continue
		}
		values, err := platformStoryInstancesInStore(ctx, store)
		store.Close()
		if err != nil {
			slog.WarnContext(ctx, "platform_story_instances_unavailable", "project", project.ID, "error", err)
			continue
		}
		items = append(items, values...)
	}
	return items, nil
}

func platformStoryInstancesInStore(ctx context.Context, store *StoryStore) ([]platform.Instance, error) {
	index, err := store.Index()
	if err != nil {
		return nil, err
	}
	projectID := store.Operation.Layout().ProjectID
	items := []platform.Instance{}
	for _, story := range index.Stories {
		addresses, err := store.ExtensionRecordAddresses(story.ID, platformStoryBindingOwner)
		if err != nil {
			slog.WarnContext(ctx, "platform_story_binding_addresses_unavailable", "project", projectID, "story", story.ID, "error", err)
			continue
		}
		for _, address := range addresses {
			record, _, err := store.ExtensionRecord(ctx, story.ID, "", address)
			if err != nil {
				slog.WarnContext(ctx, "platform_story_binding_read_failed", "project", projectID, "story", story.ID, "game", address.Key, "error", err)
				continue
			}
			var binding platformStoryBinding
			if err := json.Unmarshal(record.Value, &binding); err != nil {
				slog.WarnContext(ctx, "platform_story_binding_invalid_json", "project", projectID, "story", story.ID, "game", address.Key, "error", err)
				continue
			}
			if binding.Removed {
				continue
			}
			if binding.Instance.ProjectID != projectID || binding.Instance.StoryID != story.ID || binding.Instance.GameID != address.Key {
				slog.WarnContext(ctx, "platform_story_binding_identity_mismatch", "project", projectID, "story", story.ID, "game", address.Key)
				continue
			}
			items = append(items, binding.Instance)
		}
	}
	return items, nil
}

func (h Stories) Bind(ctx context.Context, instance platform.Instance, options platform.StoryBindingOptions) (platform.Instance, error) {
	operation, err := h.host.AcquireStory(ctx, instance.ProjectID)
	if err != nil {
		return platform.Instance{}, err
	}
	defer operation.Release()
	if instance.StoryID == "" {
		story, err := h.host.CreateInteractiveStoryContext(operation.Context(), interactive.CreateStoryRequest{Title: instance.Title, Origin: options.Origin, ProfileID: options.ModelProfile, Preview: instance.Preview})
		if err != nil {
			return platform.Instance{}, err
		}
		instance.StoryID = story.ID
	} else if instance.Preview {
		return platform.Instance{}, platformStoryError("INVALID_ARGUMENT", "Preview must create an isolated Story")
	}
	store, err := h.host.OpenStoryStore(ctx, instance.ProjectID)
	if err != nil {
		return platform.Instance{}, err
	}
	defer store.Close()
	if _, err := store.Snapshot(instance.StoryID, ""); err != nil {
		return platform.Instance{}, err
	}
	instance.ID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(instance.ProjectID+"/"+instance.StoryID+"/"+instance.GameID)).String()
	address := interactive.ExtensionRecord{Owner: platformStoryBindingOwner, Key: instance.GameID}
	previous, revision, err := store.ExtensionRecord(ctx, instance.StoryID, "", address)
	if err != nil {
		return platform.Instance{}, err
	}
	if revision != 0 {
		var old platformStoryBinding
		if err := json.Unmarshal(previous.Value, &old); err != nil {
			return platform.Instance{}, err
		}
		if !old.Removed {
			return platform.Instance{}, platformStoryError("IDEMPOTENCY_CONFLICT", "This Story already has a binding for this game; open the existing journey")
		}
	}
	address.SchemaVersion = 1
	address.Value, err = json.Marshal(platformStoryBinding{Instance: instance})
	if err != nil {
		return platform.Instance{}, err
	}
	_, err = store.SetExtensionRecord(ctx, instance.StoryID, "", revision, address)
	return instance, err
}

func (h Stories) SaveBinding(ctx context.Context, instance platform.Instance) error {
	return h.writeBinding(ctx, platformStoryBinding{Instance: instance})
}

func (h Stories) RemoveBinding(ctx context.Context, instance platform.Instance) error {
	// Removal appends a reversible tombstone. Story prose, extension history and
	// assets remain available for fallback and reattachment.
	return h.writeBinding(ctx, platformStoryBinding{Instance: instance, Removed: true})
}

func (h Stories) writeBinding(ctx context.Context, binding platformStoryBinding) error {
	instance := binding.Instance
	store, err := h.host.OpenStoryStore(ctx, instance.ProjectID)
	if err != nil {
		return err
	}
	defer store.Close()
	address := interactive.ExtensionRecord{Owner: platformStoryBindingOwner, Key: instance.GameID}
	_, revision, err := store.ExtensionRecord(ctx, instance.StoryID, "", address)
	if err != nil {
		return err
	}
	if revision == 0 {
		return platformStoryError("NOT_FOUND", "Story application binding is unavailable")
	}
	address.SchemaVersion = 1
	address.Value, err = json.Marshal(binding)
	if err != nil {
		return err
	}
	_, err = store.SetExtensionRecord(ctx, instance.StoryID, "", revision, address)
	return err
}

func (h Stories) Export(ctx context.Context, instance platform.Instance, writer io.Writer) error {
	store, err := h.host.OpenStoryStore(ctx, instance.ProjectID)
	if err != nil {
		return err
	}
	defer store.Close()
	archive := zip.NewWriter(writer)
	defer archive.Close()
	metadata, err := archive.Create("export.json")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(metadata).Encode(struct {
		Version    int               `json:"version"`
		Instance   platform.Instance `json:"instance"`
		ExportedAt time.Time         `json:"exportedAt"`
	}{1, instance, time.Now().UTC()}); err != nil {
		return err
	}
	target, err := archive.Create("story.jsonl")
	if err != nil {
		return err
	}
	if err := store.ExportJournal(ctx, instance.StoryID, target); err != nil {
		return err
	}
	assets := filepath.Join(store.Operation.Layout().StoreRoot, "extensions", instance.GameID, instance.StoryID)
	if _, err := os.Stat(assets); os.IsNotExist(err) {
		return archive.Close()
	}
	if err := portablepath.PreflightTree(assets); err != nil {
		return err
	}
	err = filepath.WalkDir(assets, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(assets, path)
		if err != nil {
			return err
		}
		target, err := archive.Create("assets/" + filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(target, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	return archive.Close()
}
