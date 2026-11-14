package enpass

import (
	"database/sql"
	"strings"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// defaultTagIcon is the icon value Enpass writes on a tag folder.
//
// A tag folder created by Enpass 6.12 on macOS and Android stores "1008" in
// folder.icon; the folder title is the tag name and membership lives in
// folder_items. Unlike the item template this is a plain constant, so it is
// repeated here rather than read from the vault.
const defaultTagIcon = "1008"

// ensureTags attaches the named tags to an item, creating missing tags.
//
// Enpass does not store tags on the item. A tag is a row in folder (uuid,
// title, icon, updated_at, deleted, parent_uuid, extra) and membership is a row
// in folder_items (folder_uuid, item_uuid, updated_at, deleted, extra). This
// mirrors what the app writes: tag folders use icon "1008", parent_uuid is
// empty, and a new membership is inserted with deleted = 0.
//
// Existing tags are matched by title, so adding "work" reuses the folder the
// app already created instead of making a duplicate. Existing memberships are
// left untouched, which makes the call idempotent.
//
// Column names are filtered through pragma_table_info, so a vault with a
// different folder schema still works.
//
// Example: ensureTags(tx, itemUUID, []string{"work", "personal"}, now)
func ensureTags(q sqlExecutor, itemUUID string, tags []string, now int64) error {
	if len(tags) == 0 {
		return nil
	}

	folderColumns, err := tableColumns(q, "folder")
	if err != nil {
		return err
	}
	linkColumns, err := tableColumns(q, "folder_items")
	if err != nil {
		return err
	}

	for _, raw := range tags {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}

		folderUUID, err := ensureTagFolder(q, folderColumns, name, now)
		if err != nil {
			return errors.Wrapf(err, "could not ensure tag %q", name)
		}

		linked, err := tagLinkExists(q, folderUUID, itemUUID)
		if err != nil {
			return errors.Wrapf(err, "could not check tag %q", name)
		}
		if linked {
			continue
		}

		err = insertRow(q, "folder_items", linkColumns, map[string]interface{}{
			"folder_uuid": folderUUID,
			"item_uuid":   itemUUID,
			"updated_at":  now,
			"deleted":     0,
			"extra":       "",
		})
		if err != nil {
			return errors.Wrapf(err, "could not link tag %q", name)
		}
	}

	return nil
}

// ensureTagFolder returns the uuid of the named tag, creating the folder when
// the vault does not have it yet.
func ensureTagFolder(q sqlExecutor, columns map[string]bool, name string, now int64) (string, error) {
	var folderUUID string
	err := q.QueryRow(
		"SELECT uuid FROM folder WHERE title = ? AND deleted = 0 LIMIT 1", name,
	).Scan(&folderUUID)

	if err == nil {
		return folderUUID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	folderUUID = uuid.New().String()
	err = insertRow(q, "folder", columns, map[string]interface{}{
		"uuid":        folderUUID,
		"title":       name,
		"icon":        defaultTagIcon,
		"updated_at":  now,
		"deleted":     0,
		"parent_uuid": "",
		"extra":       "",
	})

	return folderUUID, err
}

// tagLinkExists reports whether the item is already tagged with folderUUID.
//
// A soft-deleted link (deleted = 1) counts as absent, so re-tagging revives the
// membership instead of adding a second row.
func tagLinkExists(q sqlExecutor, folderUUID string, itemUUID string) (bool, error) {
	var found int
	err := q.QueryRow(
		`SELECT 1 FROM folder_items
		 WHERE folder_uuid = ? AND item_uuid = ? AND deleted = 0 LIMIT 1`,
		folderUUID, itemUUID,
	).Scan(&found)

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}
