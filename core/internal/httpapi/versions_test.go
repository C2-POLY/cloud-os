package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestVersionsThroughTheAPI(t *testing.T) {
	enzo, johan := newFilesAPI(t)
	enzo.upload("plan.txt", "v1", nil)
	if rec := enzo.upload("plan.txt", "v2", nil); rec.Code != http.StatusConflict {
		t.Fatalf("upload over a file without replace: %d", rec.Code)
	}
	if rec := enzo.upload("plan.txt&replace=1", "v2", nil); rec.Code != http.StatusCreated {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}

	rec := enzo.get("/api/files/versions?path=plan.txt", nil)
	var versions []VersionEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &versions); err != nil || len(versions) != 1 || versions[0].Size != 2 {
		t.Fatalf("versions: %d %s %v", rec.Code, rec.Body, err)
	}
	id := versions[0].ID
	if rec := enzo.get(fmt.Sprintf("/api/files/versions/%d/content?name=plan.txt", id), nil); rec.Body.String() != "v1" {
		t.Errorf("version content %q", rec.Body)
	}
	if rec := johan.get(fmt.Sprintf("/api/files/versions/%d/content", id), nil); rec.Code != http.StatusNotFound {
		t.Errorf("johan reads enzo's version: %d", rec.Code)
	}
	if rec := johan.do("POST", fmt.Sprintf("/api/files/versions/%d/restore", id), `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("johan restores enzo's version: %d", rec.Code)
	}
	if rec := enzo.do("POST", fmt.Sprintf("/api/files/versions/%d/restore", id), `{}`); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.get("/api/files/content?path=plan.txt", nil); rec.Body.String() != "v1" {
		t.Errorf("current after restore %q", rec.Body)
	}
	if rec := enzo.get("/api/files/versions?path=plan.txt", nil); json.Unmarshal(rec.Body.Bytes(), &versions) != nil || len(versions) != 2 {
		t.Errorf("versions after restore: %s", rec.Body)
	}
}
