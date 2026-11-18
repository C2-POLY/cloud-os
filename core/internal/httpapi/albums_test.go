package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

func TestAlbumsAPI(t *testing.T) {
	enzo, johan := newFilesAPI(t)
	for _, p := range []string{"Photos/plage.jpg", "Photos/phare.jpg"} {
		if rec := enzo.upload(p, "image "+p, nil); rec.Code != http.StatusCreated {
			t.Fatalf("upload %s: %d", p, rec.Code)
		}
	}
	rec := enzo.do("POST", "/api/albums", `{"name":"Bretagne"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var a AlbumEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || a.Name != "Bretagne" || a.Count != 0 || a.Cover != nil {
		t.Fatalf("created = %+v, %v", a, err)
	}
	base := "/api/albums/" + strconv.FormatInt(a.ID, 10)
	if rec := enzo.do("POST", "/api/albums", `{"name":"bretagne"}`); rec.Code != http.StatusConflict {
		t.Errorf("same name: %d, want 409", rec.Code)
	}
	if rec := enzo.do("POST", base+"/items", `{"paths":["Photos/plage.jpg","Photos/phare.jpg"]}`); rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.do("POST", base+"/items/remove", `{"paths":["Photos/phare.jpg"]}`); rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	rec = enzo.get(base+"/items?tz=Europe/Paris", nil)
	var page TimelinePage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].Path != "Photos/plage.jpg" {
		t.Fatalf("items = %d %s", rec.Code, rec.Body)
	}
	if rec := enzo.do("PATCH", base, `{"name":"Bretagne 2024"}`); rec.Code != http.StatusOK {
		t.Errorf("rename: %d %s", rec.Code, rec.Body)
	}
	rec = enzo.get("/api/albums", nil)
	var all []AlbumEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil || len(all) != 1 || all[0].Name != "Bretagne 2024" || all[0].Count != 1 || all[0].Cover == nil || all[0].Cover.Path != "Photos/plage.jpg" {
		t.Fatalf("list = %s", rec.Body)
	}

	// Another account sees nothing of it and cannot change it.
	for _, r := range []struct{ method, path, body string }{
		{"GET", base, ""}, {"GET", base + "/items", ""}, {"PATCH", base, `{"name":"x"}`}, {"DELETE", base, "{}"},
		{"POST", base + "/items", `{"paths":["Photos/plage.jpg"]}`},
	} {
		if rec := johan.do(r.method, r.path, r.body); rec.Code != http.StatusNotFound {
			t.Errorf("johan %s %s: %d, want 404", r.method, r.path, rec.Code)
		}
	}

	if rec := enzo.do("DELETE", base, "{}"); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := enzo.get("/api/files/content?path=Photos/plage.jpg", nil); rec.Code != http.StatusOK {
		t.Errorf("the photo after deleting its album: %d", rec.Code)
	}
}
