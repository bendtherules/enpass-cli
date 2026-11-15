package enpass

import (
	"database/sql"
	"sort"
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

// Tag is one top-level folder row plus its live membership count.
//
// Enpass stores tags and ordinary folders in the same `folder` table; the icon
// is the only discriminator. The app and this writer use "1008" for tags, so
// IsTag reports that without hiding ordinary folders when the caller asked for
// them.
type Tag struct {
	UUID  string `json:"uuid"`
	Title string `json:"title"`
	Items int    `json:"items"`
	IsTag bool   `json:"is_tag"`
}

// ListTags returns every live top-level folder with its membership count.
//
// includeFolders=false keeps only tags (icon "1008"), which is what the `tags`
// command shows by default; true also returns ordinary folders, tags first.
//
// Example:
//
//	tags, err := vault.ListTags(false)
//	// [{"uuid":"…","title":"work","items":2,"is_tag":true}]
func (v *Vault) ListTags(includeFolders bool) ([]Tag, error) {
	if v.db == nil {
		return nil, errors.New("vault is not initialized")
	}

	rows, err := v.db.Query(`
		SELECT f.uuid, f.title, f.icon,
		       (SELECT COUNT(*) FROM folder_items fi
		         WHERE fi.folder_uuid = f.uuid AND fi.deleted = 0)
		  FROM folder f
		 WHERE f.deleted = 0 AND f.parent_uuid = ''
		 ORDER BY f.title COLLATE NOCASE`)
	if err != nil {
		return nil, errors.Wrap(err, "could not query tags")
	}
	defer rows.Close()

	tags := make([]Tag, 0)
	for rows.Next() {
		var tag Tag
		var icon string
		if err := rows.Scan(&tag.UUID, &tag.Title, &icon, &tag.Items); err != nil {
			return nil, errors.Wrap(err, "could not read tag row")
		}
		tag.IsTag = icon == defaultTagIcon
		if tag.IsTag || includeFolders {
			tags = append(tags, tag)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "error iterating tag rows")
	}

	// Tags first, then folders, each already alphabetical from SQL.
	sort.SliceStable(tags, func(i, j int) bool {
		return tags[i].IsTag && !tags[j].IsTag
	})
	return tags, nil
}

// TagsByItem returns tag titles keyed by item UUID, sorted per item.
//
// Only tags (icon "1008") are included: an ordinary folder is not a tag even
// though it lives in the same table. Items without tags are absent from the
// map.
//
// Example:
//
//	byItem, err := vault.TagsByItem()
//	// {"<uuid>": ["personal","work"]}
func (v *Vault) TagsByItem() (map[string][]string, error) {
	if v.db == nil {
		return nil, errors.New("vault is not initialized")
	}

	rows, err := v.db.Query(`
		SELECT fi.item_uuid, f.title
		  FROM folder_items fi
		  JOIN folder f ON f.uuid = fi.folder_uuid AND f.deleted = 0
		 WHERE fi.deleted = 0 AND f.icon = ?
		 ORDER BY f.title COLLATE NOCASE`, defaultTagIcon)
	if err != nil {
		return nil, errors.Wrap(err, "could not query item tags")
	}
	defer rows.Close()

	byItem := make(map[string][]string)
	for rows.Next() {
		var uuid, title string
		if err := rows.Scan(&uuid, &title); err != nil {
			return nil, errors.Wrap(err, "could not read item tag row")
		}
		byItem[uuid] = append(byItem[uuid], title)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "error iterating item tags")
	}
	return byItem, nil
}
