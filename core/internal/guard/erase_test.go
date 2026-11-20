// Package guard holds tests that check the core's code itself.
package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// allowedErasers is the closed list of functions that may erase or truncate a
// file, and why each one cannot touch a stored file. Adding a function here is
// a design decision: it needs its own review, never a quick fix to get the test
// to pass.
var allowedErasers = map[string]string{
	"storage.Put":                 "removes its own temporary file under .cloudos/tmp once linked",
	"storage.writeTemp":           "removes its own temporary file when the write fails",
	"storage.CleanTmp":            "empties .cloudos/tmp at startup",
	"storage.appendBytes":         "cuts an upload's partial file back to its offset",
	"storage.endUpload":           "removes an upload's partial file under .cloudos/uploads",
	"storage.CleanUploads":        "removes expired or orphan partial files under .cloudos/uploads",
	"storage.removeTrashDir":      "the trash purge: erases one item folder, strictly inside .cloudos/trash/<owner>",
	"storage.removeEmptyTrashDir": "removes an emptied trash folder; os.Remove fails on a folder that is not empty",
	"storage.removeVersionFile":   "the version retention: erases one version beyond it, strictly at .cloudos/versions/<owner>/<file>/<version>",
	"storage.removeVersionsDir":   "the trash purge of a file: erases its versions, strictly at .cloudos/versions/<owner>/<file>",
	"storage.commitPending":       "removes its own temporary file under .cloudos/tmp once linked (WebDAV writes)",
	"storage.dropPending":         "removes its own temporary file under .cloudos/tmp when a WebDAV write is aborted",
	"davfs.removeAppleFile":       "erases a Finder file (.DS_Store, ._*) under .cloudos/appledouble/<owner>, never a user document",
	"smbsrv.setEndOfFile":         "SMB's SET_INFO end of file: calls the Backend's File.Truncate, which smbfs refuses with ErrReadOnly",
	"smbsrv.timewarp":             "time.Time.Truncate: rounds the time of a snapshot, erases nothing",
}

// notLinked are folders of the core's tree that hold code the core never
// links: smbsrv's own backend over local folders, which erases files of its
// own. TestOnlyAllowedFunctionsEraseFiles skips them and fails if any other
// file imports them.
var notLinked = map[string]string{
	"third_party/smbsrv/localfs": "github.com/tobychui/smbsrv/localfs",
}

// erasing reports whether call can erase or truncate a file.
func erasing(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	name := sel.Sel.Name
	if pkg, ok := sel.X.(*ast.Ident); ok {
		switch pkg.Name + "." + name {
		case "os.Remove", "os.RemoveAll", "os.Truncate", "syscall.Unlink", "syscall.Rmdir", "unix.Unlink", "unix.Unlinkat", "unix.Rmdir":
			return pkg.Name + "." + name, true
		}
	}
	if name == "Truncate" { // (*os.File).Truncate and the like
		return "Truncate", true
	}
	return "", false
}

// TestOnlyAllowedFunctionsEraseFiles walks every non-test Go file of the core,
// the SMB library it keeps in third_party included, and fails on any erasing
// call outside allowedErasers.
func TestOnlyAllowedFunctionsEraseFiles(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if _, skip := notLinked[filepath.ToSlash(rel)]; skip && d.IsDir() {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			for dir, pkg := range notLinked {
				if strings.Trim(imp.Path.Value, `"`) == pkg {
					t.Errorf("%s imports %s, which the guard does not check (%s)", path, pkg, dir)
				}
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			owner := file.Name.Name + "." + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if what, ok := erasing(call); ok {
					found[owner] = true
					if _, allowed := allowedErasers[owner]; !allowed {
						t.Errorf("%s: %s calls %s, which can erase a file; only the functions in allowedErasers may",
							fset.Position(call.Pos()), owner, what)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A function that no longer erases anything leaves the list, so the list
	// stays the exact set of places to review.
	var stale []string
	for name := range allowedErasers {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("allowedErasers lists functions that erase nothing any more: %v", stale)
	}
}
