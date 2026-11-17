package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func trashList(t *testing.T, c *client) []TrashEntry {
	t.Helper()
	rec := c.get("/api/trash", nil)
	var items []TrashEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("trash %d %s: %v", rec.Code, rec.Body, err)
	}
	return items
}

func TestTrashThroughTheAPI(t *testing.T) {
	enzo, johan := newFilesAPI(t)
	enzo.upload("Docs/facture.pdf", "pdf", nil)
	enzo.upload("Docs/devis.pdf", "devis", nil)

	if rec := enzo.do("POST", "/api/files/trash", `{"path":"Docs/facture.pdf"}`); rec.Code != http.StatusOK {
		t.Fatalf("trash: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.do("POST", "/api/files/trash", `{"path":"Docs/absent.pdf"}`); rec.Code != http.StatusNotFound {
		t.Errorf("trash a missing file: %d", rec.Code)
	}
	if got := listing(t, enzo.get("/api/files?path=Docs", nil)); fmt.Sprint(got) != "[devis.pdf]" {
		t.Errorf("Docs = %v", got)
	}
	items := trashList(t, enzo)
	if len(items) != 1 || items[0].Name != "facture.pdf" || items[0].Path != "Docs/facture.pdf" {
		t.Fatalf("trash = %+v", items)
	}
	if got := trashList(t, johan); len(got) != 0 {
		t.Errorf("johan sees enzo's trash: %+v", got)
	}
	id := items[0].ID
	if rec := johan.do("POST", fmt.Sprintf("/api/trash/%d/restore", id), `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("johan restores enzo's item: %d", rec.Code)
	}

	if rec := enzo.do("POST", fmt.Sprintf("/api/trash/%d/restore", id), `{}`); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.get("/api/files/content?path=Docs/facture.pdf", nil); rec.Body.String() != "pdf" {
		t.Errorf("restored content %q", rec.Body)
	}

	// Erasing for good needs an explicit confirmation.
	enzo.do("POST", "/api/files/trash", `{"path":"Docs"}`)
	id = trashList(t, enzo)[0].ID
	for _, body := range []string{`{}`, `{"confirm":false}`} {
		if rec := enzo.do("POST", fmt.Sprintf("/api/trash/%d/purge", id), body); rec.Code != http.StatusBadRequest {
			t.Errorf("purge with %s: %d", body, rec.Code)
		}
		if rec := enzo.do("POST", "/api/trash/empty", body); rec.Code != http.StatusBadRequest {
			t.Errorf("empty with %s: %d", body, rec.Code)
		}
	}
	if len(trashList(t, enzo)) != 1 {
		t.Fatal("an unconfirmed request erased something")
	}
	if rec := johan.do("POST", fmt.Sprintf("/api/trash/%d/purge", id), `{"confirm":true}`); rec.Code != http.StatusNotFound {
		t.Errorf("johan purges enzo's item: %d", rec.Code)
	}
	if rec := enzo.do("POST", fmt.Sprintf("/api/trash/%d/purge", id), `{"confirm":true}`); rec.Code != http.StatusNoContent {
		t.Errorf("purge: %d %s", rec.Code, rec.Body)
	}
	enzo.upload("x.txt", "x", nil)
	enzo.do("POST", "/api/files/trash", `{"path":"x.txt"}`)
	if rec := enzo.do("POST", "/api/trash/empty", `{"confirm":true}`); rec.Code != http.StatusOK || rec.Body.String() != "{\"purged\":1}\n" {
		t.Errorf("empty: %d %s", rec.Code, rec.Body)
	}
	if len(trashList(t, enzo)) != 0 {
		t.Error("trash not empty")
	}
}
