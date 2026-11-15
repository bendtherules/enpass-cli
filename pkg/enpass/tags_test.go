package enpass

import (
	"os"
	"testing"

	"github.com/sirupsen/logrus"
)

// TestVault_TagsRoundTrip writes tags through the writer and reads them back
// through the reader used by the `tags` command.
//
// The pair (ListTags, TagsByItem) is the only read path for tags: Enpass stores
// them as folder rows with icon "1008" plus folder_items links, which is why
// they cannot be reconstructed from the item table alone.
func TestVault_TagsRoundTrip(t *testing.T) {
	tmpDir := copyTestVault(t)
	defer os.RemoveAll(tmpDir)

	vault, err := NewVault(tmpDir, logrus.ErrorLevel)
	if err != nil {
		t.Fatalf("vault initialization failed: %v", err)
	}
	defer vault.Close()

	if err := vault.Open(&VaultCredentials{Password: testPassword}); err != nil {
		t.Skipf("skipping test: could not open vault (environmental issue): %v", err)
	}

	uuid, err := vault.CreateEntry(&EntryData{
		Title:    "Tagged Entry",
		Username: "tagged@example.com",
		Password: "tagpassword",
		Tags:     []string{"zeta", "alpha"},
	})
	if err != nil {
		t.Fatalf("could not create tagged entry: %v", err)
	}

	tags, err := vault.ListTags(false)
	if err != nil {
		t.Fatalf("ListTags failed: %v", err)
	}

	byTitle := make(map[string]Tag, len(tags))
	for _, tag := range tags {
		byTitle[tag.Title] = tag
	}
	for _, want := range []string{"alpha", "zeta"} {
		tag, ok := byTitle[want]
		if !ok {
			t.Fatalf("tag %q missing from ListTags: %+v", want, tags)
		}
		if !tag.IsTag {
			t.Errorf("tag %q reported IsTag=false", want)
		}
		if tag.Items < 1 {
			t.Errorf("tag %q reported %d items, want >= 1", want, tag.Items)
		}
	}

	byItem, err := vault.TagsByItem()
	if err != nil {
		t.Fatalf("TagsByItem failed: %v", err)
	}
	got := byItem[uuid]
	want := []string{"alpha", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("TagsByItem[%s] = %v, want %v", uuid, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TagsByItem[%s] = %v, want %v", uuid, got, want)
		}
	}
}

// TestVault_ListTagsOrdering checks that tags come before ordinary folders when
// folders are requested. Ordinary folders share the table but not the icon.
func TestVault_ListTagsOrdering(t *testing.T) {
	tmpDir := copyTestVault(t)
	defer os.RemoveAll(tmpDir)

	vault, err := NewVault(tmpDir, logrus.ErrorLevel)
	if err != nil {
		t.Fatalf("vault initialization failed: %v", err)
	}
	defer vault.Close()

	if err := vault.Open(&VaultCredentials{Password: testPassword}); err != nil {
		t.Skipf("skipping test: could not open vault (environmental issue): %v", err)
	}

	if _, err := vault.CreateEntry(&EntryData{
		Title: "Tagged Entry", Username: "u", Password: "p", Tags: []string{"work"},
	}); err != nil {
		t.Fatalf("could not create tagged entry: %v", err)
	}

	// A plain folder: same table, different icon, so it is not a tag.
	if _, err := vault.db.Exec(
		`INSERT INTO folder (uuid, title, icon, updated_at, deleted, parent_uuid, extra)
		 VALUES (?, ?, ?, 1, 0, '', '')`,
		"00000000-0000-4000-8000-000000000001", "Archive", "1002",
	); err != nil {
		t.Fatalf("could not insert ordinary folder: %v", err)
	}

	tagsOnly, err := vault.ListTags(false)
	if err != nil {
		t.Fatalf("ListTags(false) failed: %v", err)
	}
	for _, tag := range tagsOnly {
		if tag.Title == "Archive" {
			t.Fatalf("ordinary folder returned as a tag: %+v", tagsOnly)
		}
	}

	all, err := vault.ListTags(true)
	if err != nil {
		t.Fatalf("ListTags(true) failed: %v", err)
	}
	if len(all) != len(tagsOnly)+1 {
		t.Fatalf("ListTags(true) = %+v, want one more row than %+v", all, tagsOnly)
	}
	if all[len(all)-1].Title != "Archive" || all[len(all)-1].IsTag {
		t.Fatalf("ordinary folder should sort last with IsTag=false: %+v", all)
	}
}
