package store_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/0xGurg/himay/internal/fixture"
	"github.com/0xGurg/himay/internal/store"
)

func open(t *testing.T, b *fixture.Builder) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), b.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestReadOnly(t *testing.T) {
	b := fixture.Standard(t)
	st := open(t, b)
	// The store exposes no write API; verify the underlying connection rejects writes
	// via a DSN-equivalent connection.
	db, err := sqlOpen(b.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO project (id, worktree, time_created, time_updated, sandboxes) VALUES ('x','/',0,0,'[]')`); err == nil {
		t.Fatal("write succeeded on read-only connection")
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err == nil {
		// A checkpoint on a read-only connection must not succeed in modifying the file.
		t.Log("checkpoint returned no error (no-op on read-only)")
	}
	if st.Caps.JournalMode != "wal" {
		t.Errorf("journal mode = %q, want wal", st.Caps.JournalMode)
	}
}

func TestFileUnchanged(t *testing.T) {
	b := fixture.Standard(t)
	b.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	before, _ := os.Stat(b.Path)
	st := open(t, b)
	ctx := context.Background()
	if _, err := st.Projects(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Messages(ctx, "ses_root", -1, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Stats(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(b.Path)
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("database file changed: %v/%d → %v/%d", before.ModTime(), before.Size(), after.ModTime(), after.Size())
	}
}

func TestProjectsAndPaging(t *testing.T) {
	b := fixture.New(t)
	b.Project("p", "/w")
	for i := 0; i < 25; i++ {
		b.Session(fixture.Session{ID: "s" + string(rune('a'+i)), Project: "p", Title: "t", Updated: fixture.T0 + int64(i%5)})
	}
	st := open(t, b)
	ctx := context.Background()
	ps, err := st.Projects(ctx)
	if err != nil || len(ps) != 1 || ps[0].Sessions != 25 {
		t.Fatalf("projects = %+v, %v", ps, err)
	}
	seen := map[string]bool{}
	var cur store.Cursor
	for page := 0; page < 10; page++ {
		list, err := st.RootSessions(ctx, "p", cur, 7, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range list {
			if seen[s.ID] {
				t.Fatalf("duplicate %s across pages", s.ID)
			}
			seen[s.ID] = true
		}
		if len(list) < 7 {
			break
		}
		last := list[len(list)-1]
		cur = store.Cursor{Updated: last.Updated.UnixMilli(), ID: last.ID}
	}
	if len(seen) != 25 {
		t.Fatalf("paged %d sessions, want 25", len(seen))
	}
}

func TestChildrenAndSession(t *testing.T) {
	st := open(t, fixture.Standard(t))
	ctx := context.Background()
	root, err := st.Session(ctx, "ses_root")
	if err != nil {
		t.Fatal(err)
	}
	if root.Children != 1 || root.Model.ID != "glm-5.2" {
		t.Errorf("root = %+v", root)
	}
	kids, err := st.Children(ctx, "ses_root")
	if err != nil || len(kids) != 1 || kids[0].ID != "ses_child" {
		t.Fatalf("children = %+v, %v", kids, err)
	}
	if _, err := st.Session(ctx, "nope"); err == nil {
		t.Error("expected not found")
	}
}

func TestMessagesBoundedAndMalformed(t *testing.T) {
	st := open(t, fixture.Standard(t))
	msgs, err := st.Messages(context.Background(), "ses_root", -1, 100)
	if err != nil {
		t.Fatal(err)
	}
	var malformed, big int
	for _, m := range msgs {
		if !m.Valid {
			malformed++
			if m.RawPreview.String == "" {
				t.Error("malformed row has no raw preview")
			}
		}
		if len(m.TextPreview.String) > 240 {
			t.Errorf("preview not bounded: %d", len(m.TextPreview.String))
		}
		if m.TextLen.Int64 > 1_000_000 {
			big++
		}
		if len(m.Files.String) > 1000 {
			t.Errorf("file bytes leaked into summary: %d bytes", len(m.Files.String))
		}
	}
	if malformed != 1 || big != 1 {
		t.Errorf("malformed=%d big=%d", malformed, big)
	}
}

func TestKeysetSeq(t *testing.T) {
	st := open(t, fixture.Standard(t))
	ctx := context.Background()
	var after int64 = -1
	total := 0
	for {
		page, err := st.Messages(ctx, "ses_root", after, 4)
		if err != nil {
			t.Fatal(err)
		}
		total += len(page)
		if len(page) < 4 {
			break
		}
		after = page[len(page)-1].Seq
	}
	if total != 22 {
		t.Fatalf("total = %d, want 22", total)
	}
}

func TestCancellation(t *testing.T) {
	st := open(t, fixture.Standard(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := st.DeepSearchChunk(ctx, "layout", 0, 100); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestDeepSearchChunks(t *testing.T) {
	st := open(t, fixture.Standard(t))
	ctx := context.Background()
	var after int64
	var hits int
	for i := 0; i < 100; i++ {
		h, hi, err := st.DeepSearchChunk(ctx, "PNPM CHECK:ALL", after, 3)
		if err != nil {
			t.Fatal(err)
		}
		hits += len(h)
		if hi == 0 {
			break
		}
		after = hi
	}
	if hits < 2 {
		t.Fatalf("hits = %d, want ≥2 (case-insensitive)", hits)
	}
}

func TestInstructions(t *testing.T) {
	st := open(t, fixture.Standard(t))
	ctx := context.Background()
	state, _, err := st.Instructions(ctx, "ses_root")
	if err != nil || state == nil {
		t.Fatalf("state = %v, %v", state, err)
	}
	if state.Initial["core/instructions"] != "h_instr" || state.Current["core/date"] != "removed" {
		t.Errorf("state = %+v", state)
	}
	txt, err := st.InstructionBlob(ctx, "h_instr")
	if err != nil || txt == "" || txt[0] == '"' {
		t.Errorf("blob not unwrapped: %q %v", txt, err)
	}
	none, _, err := st.Instructions(ctx, "ses_old")
	if err != nil || none != nil {
		t.Errorf("old session state = %v, %v", none, err)
	}
}

// TestConcurrentWriter pages while another connection appends messages (WAL).
func TestConcurrentWriter(t *testing.T) {
	b := fixture.Standard(t)
	st := open(t, b)
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			b.Msg("ses_child", "w"+time.Now().Format("150405.000000")+string(rune('a'+i%26)), "idle", `{"outcome":"succeeded"}`)
		}
	}()
	for i := 0; i < 50; i++ {
		if _, err := st.Messages(ctx, "ses_child", -1, 10); err != nil {
			t.Fatalf("read during write: %v", err)
		}
	}
	wg.Wait()
	page, err := st.Messages(ctx, "ses_child", -1, 1000)
	if err != nil || len(page) != 52 {
		t.Fatalf("after writes: %d msgs, %v", len(page), err)
	}
}
