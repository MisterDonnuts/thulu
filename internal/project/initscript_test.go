package project

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func seedInitProject(t *testing.T, s *Store, id, ws string) {
	t.Helper()
	if err := s.ImportProject(t.Context(), Project{
		ID: id, Name: id, Path: "/data/projects/" + id, RepoURL: "https://example.com/" + id + ".git",
		DefaultBranch: "main", WorkspaceID: ws, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed project %s: %v", id, err)
	}
}

var userAuthor = InitAuthor{Kind: InitAuthorUser, ID: "user-1", Label: "ana@example.com"}

func TestInitScriptRevisionsAppendAndRestore(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", testWS)

	if _, err := s.InitScript(ctx, "p1"); !errors.Is(err, ErrNoInitScript) {
		t.Fatalf("InitScript on a fresh project = %v, want ErrNoInitScript", err)
	}

	first, err := s.SaveInitScript(ctx, "p1", InitScriptInput{
		Script: "npm ci\nnpm run dev\n",
		Manifest: InitManifest{
			Params: []InitParam{{Name: "NODE_ENV", Default: "development"}},
			Ports:  []int{3000, 3000, 5173},
			Ready:  InitReady{Command: "curl -fs localhost:3000"},
		},
		Note: "  first  ",
	}, userAuthor)
	if err != nil {
		t.Fatalf("save 1: %v", err)
	}
	if first.Revision != 1 || first.Note != "first" {
		t.Fatalf("first = rev %d note %q", first.Revision, first.Note)
	}
	if got := first.Manifest; got.Workdir != "." || got.Ready.TimeoutSeconds != InitDefaultTimeoutSeconds ||
		len(got.Ports) != 2 {
		t.Fatalf("manifest not normalized: %+v", got)
	}

	agent := InitAuthor{Kind: InitAuthorAgent, ID: "key-9", Label: "agent developer · task t1 · stage in_progress"}
	second, err := s.SaveInitScript(ctx, "p1", InitScriptInput{Script: "pnpm i\npnpm dev\n", Note: "pnpm"}, agent)
	if err != nil {
		t.Fatalf("save 2: %v", err)
	}
	if second.Revision != 2 {
		t.Fatalf("second revision = %d", second.Revision)
	}

	cur, err := s.InitScript(ctx, "p1")
	if err != nil || cur.Revision != 2 || cur.AuthorKind != InitAuthorAgent || cur.AuthorLabel != agent.Label {
		t.Fatalf("current = %+v, %v", cur, err)
	}

	restored, err := s.RestoreInitScript(ctx, "p1", 1, userAuthor, "back to npm")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Revision != 3 || restored.RestoredFrom != 1 || restored.Script != first.Script ||
		restored.Manifest.Ready.Command != "curl -fs localhost:3000" {
		t.Fatalf("restored = %+v", restored)
	}

	list, err := s.ListInitScriptRevisions(ctx, "p1", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 || list[0].Revision != 3 || list[2].Revision != 1 {
		t.Fatalf("history = %+v", list)
	}
	if list[1].AuthorKind != InitAuthorAgent || list[1].Size != len("pnpm i\npnpm dev\n") {
		t.Fatalf("history[1] = %+v", list[1])
	}

	if _, err := s.InitScriptRevision(ctx, "p1", 9); !errors.Is(err, ErrNoInitScript) {
		t.Fatalf("missing revision = %v, want ErrNoInitScript", err)
	}
	if _, err := s.RestoreInitScript(ctx, "p1", 9, userAuthor, ""); !errors.Is(err, ErrNoInitScript) {
		t.Fatalf("restore of a missing revision = %v", err)
	}
}

func TestInitScriptValidation(t *testing.T) {
	ok := InitScriptInput{Script: "echo hi"}
	cases := []struct {
		name string
		edit func(*InitScriptInput)
	}{
		{"empty script", func(in *InitScriptInput) { in.Script = "  \n" }},
		{"too large", func(in *InitScriptInput) { in.Script = strings.Repeat("x", InitScriptMaxBytes+1) }},
		{"lowercase param", func(in *InitScriptInput) { in.Manifest.Params = []InitParam{{Name: "port"}} }},
		{"param starting with digit", func(in *InitScriptInput) { in.Manifest.Params = []InitParam{{Name: "1PORT"}} }},
		{"reserved param", func(in *InitScriptInput) { in.Manifest.Params = []InitParam{{Name: "OCTOPUS_X"}} }},
		{"duplicate param", func(in *InitScriptInput) {
			in.Manifest.Params = []InitParam{{Name: "A"}, {Name: "A"}}
		}},
		{"port zero", func(in *InitScriptInput) { in.Manifest.Ports = []int{0} }},
		{"port too high", func(in *InitScriptInput) { in.Manifest.Ports = []int{65536} }},
		{"timeout too long", func(in *InitScriptInput) { in.Manifest.Ready.TimeoutSeconds = InitMaxTimeoutSeconds + 1 }},
		{"negative timeout", func(in *InitScriptInput) { in.Manifest.Ready.TimeoutSeconds = -1 }},
		{"absolute workdir", func(in *InitScriptInput) { in.Manifest.Workdir = "/etc" }},
		{"escaping workdir", func(in *InitScriptInput) { in.Manifest.Workdir = "app/../../x" }},
		{"long note", func(in *InitScriptInput) { in.Note = strings.Repeat("n", 501) }},
		{"setup too large", func(in *InitScriptInput) { in.Manifest.Setup = strings.Repeat("x", InitScriptMaxBytes+1) }},
		{"setup with NUL", func(in *InitScriptInput) { in.Manifest.Setup = "npm ci\x00" }},
		{"tool name with a space", func(in *InitScriptInput) { in.Manifest.Tools = map[string]string{"go lang": "1"} }},
		{"tool version injection", func(in *InitScriptInput) { in.Manifest.Tools = map[string]string{"go": "1;rm -rf /"} }},
		{"tool without version", func(in *InitScriptInput) { in.Manifest.Tools = map[string]string{"go": " "} }},
		{"tool listed twice after trim", func(in *InitScriptInput) { in.Manifest.Tools = map[string]string{"go": "1", " go": "2"} }},
		{"too many tools", func(in *InitScriptInput) {
			in.Manifest.Tools = map[string]string{}
			for i := 0; i <= initMaxTools; i++ {
				in.Manifest.Tools[strings.Repeat("t", i+1)] = "1"
			}
		}},
		{"absolute cache key", func(in *InitScriptInput) { in.Manifest.Cache.Key = []string{"/etc/passwd"} }},
		{"escaping cache path", func(in *InitScriptInput) { in.Manifest.Cache.Paths = []string{"a/../../b"} }},
		{"cache path is the workdir", func(in *InitScriptInput) { in.Manifest.Cache.Paths = []string{"./"} }},
		{"cache path is .git", func(in *InitScriptInput) { in.Manifest.Cache.Paths = []string{".git/objects"} }},
		{"cache key with a newline", func(in *InitScriptInput) { in.Manifest.Cache.Key = []string{"a\nfile:b:-"} }},
		{"cache path too long", func(in *InitScriptInput) { in.Manifest.Cache.Paths = []string{strings.Repeat("d", initMaxCachePath+1)} }},
		{"too many cache paths", func(in *InitScriptInput) {
			for i := 0; i <= initMaxCacheEntries; i++ {
				in.Manifest.Cache.Paths = append(in.Manifest.Cache.Paths, strings.Repeat("p", i+1))
			}
		}},
		{"absolute support file", func(in *InitScriptInput) { in.Manifest.Files = map[string]string{"/etc/x": ""} }},
		{"escaping support file", func(in *InitScriptInput) { in.Manifest.Files = map[string]string{"../x.sh": ""} }},
		{"support file not in clean form", func(in *InitScriptInput) { in.Manifest.Files = map[string]string{"./x.sh": ""} }},
		{"support file with trailing slash", func(in *InitScriptInput) { in.Manifest.Files = map[string]string{"dir/": ""} }},
		{"support file in .git", func(in *InitScriptInput) { in.Manifest.Files = map[string]string{".git/hooks/x": ""} }},
		{"support file with a newline", func(in *InitScriptInput) { in.Manifest.Files = map[string]string{"a\nb": ""} }},
		{"support file with NUL", func(in *InitScriptInput) { in.Manifest.Files = map[string]string{"a.bin": "\x00"} }},
		{"support file too large", func(in *InitScriptInput) {
			in.Manifest.Files = map[string]string{"big.js": strings.Repeat("x", initMaxFileBytes+1)}
		}},
		{"support files too large together", func(in *InitScriptInput) {
			in.Manifest.Files = map[string]string{}
			for i := 0; i*initMaxFileBytes <= initMaxFilesTotal; i++ {
				in.Manifest.Files[strings.Repeat("f", i+1)] = strings.Repeat("x", initMaxFileBytes)
			}
		}},
		{"too many support files", func(in *InitScriptInput) {
			in.Manifest.Files = map[string]string{}
			for i := 0; i <= initMaxFiles; i++ {
				in.Manifest.Files[strings.Repeat("f", i+1)] = ""
			}
		}},
		{"support file inside another", func(in *InitScriptInput) {
			in.Manifest.Files = map[string]string{"tools": "x", "tools/seed.mjs": "y"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := ok
			c.edit(&in)
			_, err := ValidateInitScript(in)
			if err == nil {
				t.Fatal("accepted")
			}
			if k := apperr.KindOf(err); k != apperr.KindInvalid && k != apperr.KindTooLarge {
				t.Fatalf("kind = %v, want invalid or too large", k)
			}
		})
	}

	in := ok
	in.Manifest = InitManifest{
		Params:  []InitParam{{Name: "DB_URL", Required: true}, {Name: "PORT_2", Default: "8080"}},
		Ports:   []int{1, 65535},
		Workdir: "./apps/web/",
		Ready:   InitReady{TimeoutSeconds: InitMaxTimeoutSeconds},
	}
	got, err := ValidateInitScript(in)
	if err != nil {
		t.Fatalf("valid input refused: %v", err)
	}
	if got.Manifest.Workdir != "apps/web" {
		t.Fatalf("workdir = %q, want apps/web", got.Manifest.Workdir)
	}
	if got.Manifest.HasSetupSnapshot() || got.Manifest.Cache.Key == nil || got.Manifest.Cache.Paths == nil {
		t.Fatalf("a manifest without setup = %+v", got.Manifest)
	}

	in.Manifest.Setup = "npm ci\n"
	in.Manifest.Tools = map[string]string{" node ": " 22 ", "npm:typescript": "5.6.2", "aqua:cli/cli": "latest"}
	in.Manifest.Cache = InitCache{Key: []string{"./package-lock.json", "package-lock.json", "web/go.sum"}, Paths: []string{"node_modules/", ".venv"}}
	got, err = ValidateInitScript(in)
	if err != nil {
		t.Fatalf("valid setup refused: %v", err)
	}
	if got.Manifest.Tools["node"] != "22" || len(got.Manifest.Tools) != 3 {
		t.Fatalf("tools = %v", got.Manifest.Tools)
	}
	if strings.Join(got.Manifest.Cache.Key, ",") != "package-lock.json,web/go.sum" ||
		strings.Join(got.Manifest.Cache.Paths, ",") != "node_modules,.venv" {
		t.Fatalf("cache = %+v", got.Manifest.Cache)
	}
	if !got.Manifest.HasSetupSnapshot() {
		t.Fatal("setup + cache paths should ask for snapshots")
	}
	in.Manifest.Setup = "  \n"
	if got, _ = ValidateInitScript(in); got.Manifest.Setup != "" || got.Manifest.HasSetupSnapshot() {
		t.Fatalf("a blank setup = %q", got.Manifest.Setup)
	}

	in.Manifest.Files = map[string]string{"seed.mjs": "console.log(1)\n", "sandbox/static.mjs": "", ".env.sandbox": "A=1"}
	if got, err = ValidateInitScript(in); err != nil || len(got.Manifest.Files) != 3 || got.Manifest.Files["seed.mjs"] != "console.log(1)\n" {
		t.Fatalf("valid support files = %v, %v", got.Manifest.Files, err)
	}
	in.Manifest.Files = map[string]string{}
	if got, _ = ValidateInitScript(in); got.Manifest.Files != nil {
		t.Fatalf("empty support files should drop out of the JSON, got %v", got.Manifest.Files)
	}
}

func TestInitScriptSetupFieldsStored(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", testWS)
	m := InitManifest{
		Setup: "npm ci",
		Tools: map[string]string{"node": "22"},
		Cache: InitCache{Key: []string{"package-lock.json"}, Paths: []string{"node_modules"}},
	}
	if _, err := s.SaveInitScript(ctx, "p1", InitScriptInput{Script: "npm start", Manifest: m}, userAuthor); err != nil {
		t.Fatal(err)
	}
	sc, err := s.InitScript(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Manifest.Setup != "npm ci" || sc.Manifest.Tools["node"] != "22" ||
		sc.Manifest.Cache.Key[0] != "package-lock.json" || sc.Manifest.Cache.Paths[0] != "node_modules" {
		t.Fatalf("stored manifest = %+v", sc.Manifest)
	}
	if _, err := s.db.ExecContext(ctx, s.rb(`UPDATE project_init_script_revisions SET manifest = ? WHERE project_id = ?`),
		`{"params":[],"ports":[],"ready":{"timeout_seconds":300},"workdir":"."}`, "p1"); err != nil {
		t.Fatal(err)
	}
	old, err := s.InitScript(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if old.Manifest.Setup != "" || old.Manifest.Tools != nil || old.Manifest.Cache.Key == nil || old.Manifest.Cache.Paths == nil {
		t.Fatalf("old manifest = %+v", old.Manifest)
	}
}

func TestInitScriptAuthorKindChecked(t *testing.T) {
	s := newTestStore(t)
	seedInitProject(t, s, "p1", testWS)
	_, err := s.SaveInitScript(t.Context(), "p1", InitScriptInput{Script: "true"}, InitAuthor{Kind: "robot"})
	if apperr.KindOf(err) != apperr.KindInvalid {
		t.Fatalf("unknown author kind = %v", err)
	}
}

func TestInitScriptUnknownProject(t *testing.T) {
	s := newTestStore(t)
	_, err := s.SaveInitScript(t.Context(), "nope", InitScriptInput{Script: "true"}, userAuthor)
	if apperr.KindOf(err) != apperr.KindNotFound {
		t.Fatalf("save on a missing project = %v, want not found", err)
	}
}

func TestInitScriptIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "pa", "ws-a")
	seedInitProject(t, s, "pb", "ws-b")

	if _, err := s.SaveInitScript(ctx, "pa", InitScriptInput{Script: "echo a"}, userAuthor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InitScript(ctx, "pb"); !errors.Is(err, ErrNoInitScript) {
		t.Fatalf("pb sees a script: %v", err)
	}
	if list, err := s.ListInitScriptRevisions(ctx, "pb", 0); err != nil || len(list) != 0 {
		t.Fatalf("pb history = %v, %v", list, err)
	}
	if _, err := s.InitScriptRevision(ctx, "pb", 1); !errors.Is(err, ErrNoInitScript) {
		t.Fatalf("pb revision 1 = %v", err)
	}
	var ws string
	if err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT workspace_id FROM project_init_script_revisions WHERE project_id = ?`), "pa").Scan(&ws); err != nil {
		t.Fatal(err)
	}
	if ws != "ws-a" {
		t.Fatalf("revision workspace = %q, want ws-a", ws)
	}

	if err := s.DeleteInitScripts(ctx, "pa"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InitScript(ctx, "pa"); !errors.Is(err, ErrNoInitScript) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestInitScriptConcurrentSaves(t *testing.T) {
	s := newTestStore(t)
	seedInitProject(t, s, "p1", testWS)
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SaveInitScript(t.Context(), "p1", InitScriptInput{Script: "true"}, userAuthor)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent save: %v", err)
		}
	}
	list, err := s.ListInitScriptRevisions(t.Context(), "p1", 0)
	if err != nil || len(list) != n || list[0].Revision != n {
		t.Fatalf("history = %d entries (newest %v), %v", len(list), list, err)
	}
}

func TestImportInitScriptIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", testWS)
	when := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sc := InitScript{
		ProjectID: "p1", Revision: 7, Script: "make up", Manifest: InitManifest{Ports: []int{8080}},
		AuthorKind: InitAuthorAgent, AuthorLabel: "agent developer · task t · stage s", Note: "n", CreatedAt: when,
	}
	wrote, err := s.ImportInitScript(ctx, sc)
	if err != nil || !wrote {
		t.Fatalf("first import = %v, %v", wrote, err)
	}
	wrote, err = s.ImportInitScript(ctx, sc)
	if err != nil || wrote {
		t.Fatalf("second import = %v, %v; want a no-op", wrote, err)
	}
	cur, err := s.InitScript(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Revision != 1 || cur.AuthorKind != InitAuthorAgent || !cur.CreatedAt.Equal(when) {
		t.Fatalf("imported = %+v", cur)
	}
}

func TestImportProjectMovesInitScriptWorkspace(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "p1", "ws-a")
	for _, body := range []string{"echo 1", "echo 2"} {
		if _, err := s.SaveInitScript(ctx, "p1", InitScriptInput{Script: body}, userAuthor); err != nil {
			t.Fatal(err)
		}
	}
	seedInitProject(t, s, "p1", "ws-b")
	if _, err := s.ImportInitScript(ctx, InitScript{ProjectID: "p1", Script: "echo 3", AuthorKind: InitAuthorUser}); err != nil {
		t.Fatal(err)
	}
	var stale, total int
	if err := s.db.QueryRowContext(ctx, s.rb(
		`SELECT COUNT(*) FILTER (WHERE workspace_id <> 'ws-b'), COUNT(*)
		   FROM project_init_script_revisions WHERE project_id = ?`), "p1").Scan(&stale, &total); err != nil {
		t.Fatal(err)
	}
	if total != 3 || stale != 0 {
		t.Fatalf("revisions = %d, still in another workspace = %d; want 3 and 0", total, stale)
	}
}
