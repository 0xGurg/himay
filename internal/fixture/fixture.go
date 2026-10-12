// Package fixture builds synthetic OpenCode databases for tests. The DDL is
// copied from an installed OpenCode 2.0.25 database; payloads are hand-written.
// Nothing here ever reads or copies a user's real database.
package fixture

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Schema is the subset of OpenCode's schema Himay reads, verbatim from v2.0.25.
const Schema = `
CREATE TABLE project (
	id text PRIMARY KEY, worktree text NOT NULL, vcs text, name text, icon_url text, icon_color text,
	time_created integer NOT NULL, time_updated integer NOT NULL, time_initialized integer,
	sandboxes text NOT NULL, commands text, icon_url_override text, time_active integer DEFAULT 0 NOT NULL);
CREATE TABLE session_v2 (
	id text PRIMARY KEY, project_id text NOT NULL, workspace_id text, parent_id text, fork_session_id text,
	fork_boundary text, slug text NOT NULL, directory text NOT NULL, path text, title text, version text NOT NULL,
	share_url text, summary_additions integer, summary_deletions integer, summary_files integer, summary_diffs text,
	metadata text, cost real DEFAULT 0 NOT NULL, tokens_input integer DEFAULT 0 NOT NULL,
	tokens_output integer DEFAULT 0 NOT NULL, tokens_reasoning integer DEFAULT 0 NOT NULL,
	tokens_cache_read integer DEFAULT 0 NOT NULL, tokens_cache_write integer DEFAULT 0 NOT NULL,
	revert text, permission text, agent text, model text, time_created integer NOT NULL,
	time_updated integer NOT NULL, time_compacting integer, time_archived integer, time_suspended integer,
	resume_attempts integer DEFAULT 0 NOT NULL, time_idle integer, time_viewed integer, idle_outcome text,
	CONSTRAINT fk_session_v2_project_id_project_id_fk FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE CASCADE);
CREATE TABLE session_message (
	id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL, seq integer NOT NULL,
	time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL,
	CONSTRAINT fk_session_message_session_id_session_v2_id_fk FOREIGN KEY (session_id) REFERENCES session_v2(id) ON DELETE CASCADE);
CREATE UNIQUE INDEX session_message_session_seq_idx ON session_message (session_id,seq);
CREATE INDEX session_message_session_type_seq_idx ON session_message (session_id,type,seq);
CREATE INDEX session_message_session_time_created_id_idx ON session_message (session_id,time_created,id);
CREATE INDEX session_message_time_created_idx ON session_message (time_created);
CREATE INDEX session_v2_project_idx ON session_v2 (project_id);
CREATE INDEX session_v2_parent_idx ON session_v2 (parent_id);
CREATE TABLE instruction_blob (hash text PRIMARY KEY, value text);
CREATE TABLE instruction_entry (
	session_id text NOT NULL, key text NOT NULL, value text, removed integer DEFAULT false NOT NULL,
	time_created integer NOT NULL, time_updated integer NOT NULL,
	CONSTRAINT instruction_entry_pk PRIMARY KEY(session_id, key));
CREATE TABLE instruction_state (
	session_id text PRIMARY KEY, epoch_start integer NOT NULL, through_seq integer NOT NULL,
	initial_values text NOT NULL, current_values text NOT NULL);
CREATE TABLE session_pending (
	id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL, data text NOT NULL, delivery text,
	admitted_seq integer NOT NULL, time_created integer NOT NULL);
CREATE TABLE kv (key text PRIMARY KEY, value text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL);
`

// T0 is the fixture epoch (ms).
const T0 = int64(1_790_000_000_000)

// Builder writes a fixture database.
type Builder struct {
	DB   *sql.DB
	Path string
	t    testing.TB
	seq  map[string]int64
}

// New creates an empty WAL-mode fixture DB in a temp dir.
func New(t testing.TB) *Builder {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(wal)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Builder{DB: db, Path: path, t: t, seq: map[string]int64{}}
}

func (b *Builder) exec(q string, args ...any) {
	b.t.Helper()
	if _, err := b.DB.Exec(q, args...); err != nil {
		b.t.Fatalf("%v\n%s", err, q)
	}
}

func (b *Builder) Project(id, worktree string) {
	b.exec(`INSERT INTO project (id, worktree, time_created, time_updated, sandboxes) VALUES (?,?,?,?,'[]')`,
		id, worktree, T0, T0)
}

type Session struct {
	ID, Project, Parent, Title, Agent, Model, Version string
	Updated                                           int64
}

func (b *Builder) Session(s Session) {
	if s.Version == "" {
		s.Version = "2.0.25"
	}
	if s.Updated == 0 {
		s.Updated = T0 + 1000
	}
	var parent any
	if s.Parent != "" {
		parent = s.Parent
	}
	b.exec(`INSERT INTO session_v2 (id, project_id, parent_id, slug, directory, title, version, agent, model,
		time_created, time_updated) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Project, parent, s.ID, "/work/"+s.Project, s.Title, s.Version, s.Agent, s.Model, T0, s.Updated)
}

// Msg appends a session_message with the next seq (seq gaps of 2 mimic OpenCode).
func (b *Builder) Msg(session, id, typ, data string) int64 {
	b.seq[session] += 2
	seq := b.seq[session]
	ts := T0 + seq*1000
	b.exec(`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
		VALUES (?,?,?,?,?,?,?)`, id, session, typ, seq, ts, ts, data)
	return seq
}

func (b *Builder) Exec(q string, args ...any) { b.exec(q, args...) }

// Standard builds the canonical fixture covering every known shape and edge case.
func Standard(t testing.TB) *Builder {
	b := New(t)
	b.Project("p1", "/work/p1")
	b.Project("p2", "/work/p2")
	m := `{"id":"glm-5.2","providerID":"opencode-go","variant":"default"}`
	b.Session(Session{ID: "ses_root", Project: "p1", Title: "Fix layout", Agent: "orchestrator", Model: m, Updated: T0 + 9000})
	b.Session(Session{ID: "ses_child", Project: "p1", Parent: "ses_root", Title: "explore layout", Agent: "explorer",
		Model: `{"id":"kimi-k2.6","providerID":"opencode-go"}`})
	b.Session(Session{ID: "ses_old", Project: "p2", Title: "Old session", Agent: "build", Version: "1.0.133"})
	b.Session(Session{ID: "ses_empty", Project: "p2", Title: "", Agent: "", Version: "1.3.0"})

	s := "ses_root"
	b.Msg(s, "m01", "user", `{"text":"Fix the layout please","time":{"created":1},"files":[{"data":"`+strings.Repeat("A", 4000)+`","mime":"image/png","name":"shot.png"}],"agents":[{"name":"explorer","mention":{"text":"@explorer","start":0,"end":9}}],"skills":[{"id":"sk1","name":"planning"}]}`)
	b.Msg(s, "m01b", "synthetic", `{"text":"Called the Read tool with the following input: {\"filePath\":\"/work/p1/PLAN.md\"}","time":{"created":1}}`)
	b.Msg(s, "m02", "system", `{"text":"The available tools have changed.\n\nThe `+"`bash`"+` tool is now `+"`shell`"+`.","time":{"created":1}}`)
	b.Msg(s, "m03", "assistant", fmt.Sprintf(`{"time":{"created":%d,"completed":%d},"agent":"orchestrator","model":%s,"finish":"tool-calls","cost":0.0125,"tokens":{"input":1000,"output":200,"reasoning":50,"cache":{"read":5000,"write":0}},"content":[
		{"type":"reasoning","text":"I should read the AGENTS file first.","time":{"created":%d}},
		{"type":"tool","id":"call1","name":"read","state":{"status":"completed","input":{"filePath":"/work/p1/src/app.tsx"},"content":[{"type":"text","text":"file body\n<system-reminder>\nInstructions from: /work/p1/AGENTS.md\nAlways run `+"`pnpm check:all`"+` before finishing.\nPrefer the design tokens in src/theme over raw hex colors in every component.\n</system-reminder>"}],"metadata":{"preview":"x","truncated":false,"loaded":["/work/p1/AGENTS.md"]}},"time":{"created":%d,"completed":%d}},
		{"type":"tool","id":"call2","name":"task","state":{"status":"completed","input":{"description":"explore layout","subagent_type":"explorer","prompt":"look"},"content":[{"type":"text","text":"done"}],"metadata":{"sessionId":"ses_child","parentSessionId":"ses_root","model":{"providerID":"opencode-go","modelID":"kimi-k2.6"}}},"time":{"created":%d,"completed":%d}}
	]}`, T0+6000, T0+9000, m, T0+6100, T0+6200, T0+6300, T0+6400, T0+8000))
	b.Msg(s, "m04", "synthetic", `{"text":"Instructions from: /work/p1/src/AGENTS.md\nUse tabs.","description":"Loaded src/AGENTS.md","metadata":{"instruction":{"paths":["/work/p1/src/AGENTS.md"]}},"time":{"created":1}}`)
	b.Msg(s, "m05", "assistant", fmt.Sprintf(`{"time":{"created":%d,"completed":%d},"agent":"orchestrator","model":%s,"finish":"tool-calls","cost":0.01,"tokens":{"input":2000,"output":100,"reasoning":0,"cache":{"read":0,"write":0}},"content":[
		{"type":"reasoning","text":"","state":{"reasoningEncryptedContent":"gAAAA"}},
		{"type":"text","text":"Running checks now."},
		{"type":"tool","id":"call3","name":"bash","state":{"status":"completed","input":{"command":"pnpm check:all","description":"run checks"},"content":[{"type":"text","text":"ok \u001b[31mred\u001b[0m"}],"metadata":{"exit":0}},"time":{"created":1,"completed":2}},
		{"type":"tool","id":"call4","name":"edit","state":{"status":"completed","input":{"filePath":"/work/p1/src/app.tsx","oldString":"a","newString":"b"},"content":[{"type":"text","text":"Edit applied"}],"metadata":{"diff":"--- a\n+++ b\n@@ -1 +1 @@\n-a\n+b\n"}},"time":{"created":1,"completed":2}},
		{"type":"tool","id":"call5","name":"read","state":{"status":"error","input":{"filePath":"/nope"},"error":{"type":"tool.execution","message":"File not found: /nope"}},"time":{"created":1,"completed":2}},
		{"type":"tool","id":"call6","name":"skill","state":{"status":"completed","input":{"name":"planning"},"content":[{"type":"text","text":"<skill_content name=\"planning\">Write the plan to `+"`PLAN.md`"+` and keep it short.</skill_content>"}]},"time":{"created":1,"completed":2}},
		{"type":"mystery","foo":1}
	]}`, T0+10000, T0+12000, m))
	b.Msg(s, "m06", "assistant", `{"time":{"created":1},"agent":"orchestrator","model":{"id":"glm-5.2","providerID":"opencode-go"},"finish":"error","content":[],"error":{"type":"provider.error","message":"quota exceeded"}}`)
	b.Msg(s, "m07", "assistant", `{"time":{"created":1,"completed":2},"agent":"orchestrator","model":{"id":"glm-5.2","providerID":"opencode-go"},"finish":"stop","content":[{"type":"text","text":"Retried ok."}],"retry":{"attempt":2,"at":1790000000000,"error":{"type":"provider.error","message":"overloaded"}}}`)
	b.Msg(s, "m08", "model-switched", `{"time":{"created":1},"model":{"id":"gpt-5.6-luna","providerID":"openai"},"previous":{"id":"glm-5.2","providerID":"opencode-go","variant":"default"}}`)
	b.Msg(s, "m09", "agent-switched", `{"time":{"created":1},"agent":"fixer","previous":"orchestrator"}`)
	b.Msg(s, "m10", "assistant", `{"time":{"created":1,"completed":2},"agent":"fixer","model":{"id":"gpt-5.6-luna","providerID":"openai","variant":"high"},"finish":"stop","cost":0,"tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}},"content":[{"type":"text","text":"Fixed per AGENTS: ran pnpm check:all. Prefer the design tokens in src/theme over raw hex colors in every component."}]}`)
	b.Msg(s, "m11", "skill", `{"time":{"created":1},"skill":"sk2","name":"review","text":"Review carefully."}`)
	b.Msg(s, "m12", "shell", `{"time":{"created":1,"completed":2},"shellID":"sh_1","command":"ls -la","status":"exited","exit":0,"output":{"output":"total 0","cursor":0,"size":7,"truncated":false}}`)
	b.Msg(s, "m13", "location-switched", `{"time":{"created":1},"location":"/work/p1/sub","previous":{"location":"/work/p1"}}`)
	b.Msg(s, "m14", "compaction", `{"time":{"created":1},"status":"completed","reason":"auto","summary":"## Goal\nFix layout","recent":"m10"}`)
	b.Msg(s, "m15", "compaction", `{"time":{"created":1},"status":"failed","reason":"manual","error":{"type":"provider.error","message":"too big"}}`)
	b.Msg(s, "m16", "system", `{"text":"Instructions updated: /work/p1/AGENTS.md","metadata":{"notice":"instructions","instructionSources":["core/instructions"]},"time":{"created":1}}`)
	b.Msg(s, "m17", "idle", `{"time":{"created":1},"outcome":"succeeded"}`)
	b.Msg(s, "m18", "idle", `{"time":{"created":1},"outcome":"interrupted"}`)
	b.Msg(s, "m19", "future-thing", `{"time":{"created":1},"widget":42}`)
	b.Msg(s, "m20", "assistant", `{"agent":"orchestrator","content":[{"type":"text","text":"half-writ`) // malformed
	b.Msg(s, "m21", "user", `{"text":"`+strings.Repeat("big ", 800_000)+`","time":{"created":1}}`)      // ~3 MB

	b.Msg("ses_child", "c01", "user", `{"text":"look","time":{"created":1}}`)
	b.Msg("ses_child", "c02", "assistant", `{"time":{"created":1,"completed":2},"agent":"explorer","model":{"id":"kimi-k2.6","providerID":"opencode-go"},"finish":"stop","cost":0.002,"tokens":{"input":300,"output":30,"reasoning":0,"cache":{"read":0,"write":0}},"content":[{"type":"text","text":"Found it."}]}`)

	b.Msg("ses_old", "o01", "user", `{"text":"old request","time":{"created":1}}`)

	// instruction_state for ses_root, including core/instructions (AGENTS.md text).
	b.Exec(`INSERT INTO instruction_blob VALUES ('h_instr', ?), ('h_env', ?), ('h_env2', ?)`,
		`"Instructions from: /home/u/.config/opencode/AGENTS.md\nNever use `+"`git push --force`"+`.\n"`,
		`"<env>\n  Working directory: /work/p1\n</env>"`, `"<env>\n  Working directory: /work/p1/sub\n</env>"`)
	b.Exec(`INSERT INTO instruction_state VALUES ('ses_root', 1, 30, ?, ?)`,
		`{"core/instructions":"h_instr","core/environment":"h_env"}`,
		`{"core/instructions":"h_instr","core/environment":"h_env2","core/date":"removed"}`)
	return b
}
