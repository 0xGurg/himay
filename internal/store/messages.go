package store

import (
	"context"
	"database/sql"
	"fmt"
)

// RawMessage is a session_message row with large payloads reduced to
// bounded previews by SQLite. Every JSON column is optional.
type RawMessage struct {
	ID, Type          string
	Seq               int64
	Created, Updated  sql.NullInt64
	Valid             bool
	Agent             sql.NullString
	Model             sql.NullString // JSON
	Cost              sql.NullFloat64
	Tokens            sql.NullString // JSON
	Finish            sql.NullString
	Error             sql.NullString // JSON
	Retry             sql.NullString // JSON
	Time              sql.NullString // JSON
	Outcome           sql.NullString
	Status            sql.NullString
	Reason            sql.NullString
	Previous          sql.NullString // JSON
	Description       sql.NullString
	Metadata          sql.NullString // JSON, capped
	TextPreview       sql.NullString
	TextLen           sql.NullInt64
	ContentCount      sql.NullInt64
	Agents            sql.NullString // JSON array of names
	Skills            sql.NullString // JSON array of names
	Files             sql.NullString // JSON array of {name,mime,size}
	Keys              sql.NullString // JSON array of top-level keys
	SkillName         sql.NullString
	ShellCommand      sql.NullString
	ModelSwitchTarget sql.NullString // JSON
	Location          sql.NullString
	RawPreview        sql.NullString // only for malformed rows
}

// previewLen bounds every text preview pulled out of SQLite.
const previewLen = 240

const messageCols = `id, type, seq, time_created, time_updated, json_valid(data),
	CASE WHEN json_valid(data) THEN json_extract(data,'$.agent') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.model') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.cost') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.tokens') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.finish') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.error') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.retry') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.time') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.outcome') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.status') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.reason') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.previous') END,
	CASE WHEN json_valid(data) THEN substr(json_extract(data,'$.description'),1,240) END,
	CASE WHEN json_valid(data) THEN substr(json_extract(data,'$.metadata'),1,4000) END,
	CASE WHEN json_valid(data) THEN substr(COALESCE(json_extract(data,'$.text'), json_extract(data,'$.summary')),1,240) END,
	CASE WHEN json_valid(data) THEN length(COALESCE(json_extract(data,'$.text'), json_extract(data,'$.summary'))) END,
	CASE WHEN json_valid(data) THEN json_array_length(data,'$.content') END,
	CASE WHEN json_valid(data) THEN (SELECT json_group_array(json_extract(a.value,'$.name')) FROM json_each(data,'$.agents') a) END,
	CASE WHEN json_valid(data) THEN (SELECT json_group_array(COALESCE(json_extract(k.value,'$.name'), json_extract(k.value,'$.id'))) FROM json_each(data,'$.skills') k) END,
	CASE WHEN json_valid(data) THEN (SELECT json_group_array(json_object('name', COALESCE(json_extract(f.value,'$.name'), json_extract(f.value,'$.source.uri'), ''),
		'mime', COALESCE(json_extract(f.value,'$.mime'), ''), 'size', length(json_extract(f.value,'$.data')))) FROM json_each(data,'$.files') f) END,
	CASE WHEN json_valid(data) AND json_type(data)='object' THEN (SELECT json_group_array(k.key) FROM json_each(data) k) END,
	CASE WHEN json_valid(data) THEN COALESCE(json_extract(data,'$.name'), json_extract(data,'$.skill')) END,
	CASE WHEN json_valid(data) THEN substr(json_extract(data,'$.command'),1,240) END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.model') END,
	CASE WHEN json_valid(data) THEN json_extract(data,'$.location') END,
	CASE WHEN json_valid(data) THEN NULL ELSE substr(data,1,400) END`

func scanRawMessage(rows *sql.Rows) (RawMessage, error) {
	var r RawMessage
	err := rows.Scan(&r.ID, &r.Type, &r.Seq, &r.Created, &r.Updated, &r.Valid,
		&r.Agent, &r.Model, &r.Cost, &r.Tokens, &r.Finish, &r.Error, &r.Retry, &r.Time,
		&r.Outcome, &r.Status, &r.Reason, &r.Previous, &r.Description, &r.Metadata,
		&r.TextPreview, &r.TextLen, &r.ContentCount, &r.Agents, &r.Skills, &r.Files, &r.Keys,
		&r.SkillName, &r.ShellCommand, &r.ModelSwitchTarget, &r.Location, &r.RawPreview)
	return r, err
}

// Messages returns up to limit messages with seq > afterSeq, in seq order.
func (s *Store) Messages(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM session_message
		WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT ?`, sessionID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("messages: %w", err)
	}
	defer rows.Close()
	var out []RawMessage
	for rows.Next() {
		r, err := scanRawMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MessagesByID re-reads specific messages (used to refresh rows that were
// rewritten in place while streaming).
func (s *Store) MessagesByID(ctx context.Context, ids []string) ([]RawMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM session_message
		WHERE id IN (`+placeholders(len(ids))+`) ORDER BY seq`, args...)
	if err != nil {
		return nil, fmt.Errorf("messages by id: %w", err)
	}
	defer rows.Close()
	var out []RawMessage
	for rows.Next() {
		r, err := scanRawMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RawItem summarizes one entry of an assistant message's content array.
type RawItem struct {
	MsgID        string
	Index        int
	Type         sql.NullString
	Name         sql.NullString
	CallID       sql.NullString
	Status       sql.NullString
	Time         sql.NullString // JSON
	Input        sql.NullString // capped JSON/text
	Error        sql.NullString // JSON
	ChildSession sql.NullString
	ChildAgent   sql.NullString
	ChildModel   sql.NullString // JSON
	Loaded       sql.NullString // JSON array
	Files        sql.NullString // JSON array of paths
	TextPreview  sql.NullString
	TextLen      sql.NullInt64
	Encrypted    bool
}

// Items returns content summaries for assistant messages in [fromSeq, toSeq].
func (s *Store) Items(ctx context.Context, sessionID string, fromSeq, toSeq int64) ([]RawItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sm.id, CAST(c.key AS INTEGER),
			json_extract(c.value,'$.type'), json_extract(c.value,'$.name'), json_extract(c.value,'$.id'),
			json_extract(c.value,'$.state.status'), json_extract(c.value,'$.time'),
			substr(json_extract(c.value,'$.state.input'),1,600),
			json_extract(c.value,'$.state.error'),
			json_extract(c.value,'$.state.metadata.sessionId'),
			COALESCE(json_extract(c.value,'$.state.input.subagent_type'), json_extract(c.value,'$.state.input.agent')),
			json_extract(c.value,'$.state.metadata.model'),
			json_extract(c.value,'$.state.metadata.loaded'),
			CASE json_extract(c.value,'$.name')
				WHEN 'apply_patch' THEN (SELECT json_group_array(json_extract(f.value,'$.filePath')) FROM json_each(c.value,'$.state.metadata.files') f)
				WHEN 'edit' THEN json_array(COALESCE(json_extract(c.value,'$.state.input.filePath'), json_extract(c.value,'$.state.input.path')))
				WHEN 'write' THEN json_array(COALESCE(json_extract(c.value,'$.state.input.filePath'), json_extract(c.value,'$.state.input.path')))
			END,
			substr(json_extract(c.value,'$.text'),1,240), length(json_extract(c.value,'$.text')),
			json_extract(c.value,'$.state.reasoningEncryptedContent') IS NOT NULL
		FROM session_message sm, json_each(sm.data,'$.content') c
		WHERE sm.session_id = ? AND sm.type = 'assistant' AND sm.seq BETWEEN ? AND ?
			AND json_valid(sm.data)
		ORDER BY sm.seq, CAST(c.key AS INTEGER)`, sessionID, fromSeq, toSeq)
	if err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}
	defer rows.Close()
	var out []RawItem
	for rows.Next() {
		var r RawItem
		if err := rows.Scan(&r.MsgID, &r.Index, &r.Type, &r.Name, &r.CallID, &r.Status, &r.Time, &r.Input,
			&r.Error, &r.ChildSession, &r.ChildAgent, &r.ChildModel, &r.Loaded, &r.Files,
			&r.TextPreview, &r.TextLen, &r.Encrypted); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ItemJSON returns the full JSON of one content item (lazy detail load).
func (s *Store) ItemJSON(ctx context.Context, msgID string, index int) (string, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT json_extract(data, '$.content['||?||']')
		FROM session_message WHERE id = ? AND json_valid(data)`, index, msgID).Scan(&v)
	if err != nil {
		return "", fmt.Errorf("item %s[%d]: %w", msgID, index, err)
	}
	return v.String, nil
}

// MessageText returns a message's full text field (user/system/synthetic/skill/compaction summary).
func (s *Store) MessageText(ctx context.Context, msgID string) (string, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN json_valid(data)
		THEN COALESCE(json_extract(data,'$.text'), json_extract(data,'$.summary'), json_extract(data,'$.output.output'))
		ELSE data END FROM session_message WHERE id = ?`, msgID).Scan(&v)
	if err != nil {
		return "", fmt.Errorf("message text %s: %w", msgID, err)
	}
	return v.String, nil
}

// MessageJSON returns a message's raw data, excluding inline file bytes.
func (s *Store) MessageJSON(ctx context.Context, msgID string) (string, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN json_valid(data) AND json_type(data,'$.files')='array'
		THEN json_remove(data,'$.files') ELSE data END FROM session_message WHERE id = ?`, msgID).Scan(&v)
	if err != nil {
		return "", fmt.Errorf("message json %s: %w", msgID, err)
	}
	return v.String, nil
}

// MaxSeq returns the highest seq and last update time for a session (cheap refresh check).
func (s *Store) MaxSeq(ctx context.Context, sessionID string) (int64, int64, error) {
	var seq, upd sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT max(seq), max(time_updated) FROM session_message WHERE session_id = ?`,
		sessionID).Scan(&seq, &upd)
	return seq.Int64, upd.Int64, err
}

// StepText is the searchable text of one assistant content item.
type StepText struct {
	MsgID string
	Index int
	Seq   int64
	Kind  string // text | reasoning | tool
	Tool  string
	Text  string // text/reasoning body, or tool input JSON (capped)
}

// StepTexts returns capped text of assistant items with seq > afterSeq, for
// instruction correlation. Tool outputs are excluded: they are what the tool
// returned, not what the model chose to do.
func (s *Store) StepTexts(ctx context.Context, sessionID string, afterSeq int64) ([]StepText, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT sm.id, CAST(c.key AS INTEGER), sm.seq, COALESCE(json_extract(c.value,'$.type'),''),
			COALESCE(json_extract(c.value,'$.name'),''),
			COALESCE(substr(CASE json_extract(c.value,'$.type')
				WHEN 'tool' THEN json_extract(c.value,'$.state.input')
				ELSE json_extract(c.value,'$.text') END, 1, 16000), '')
		FROM session_message sm, json_each(sm.data,'$.content') c
		WHERE sm.session_id = ? AND sm.type = 'assistant' AND sm.seq > ? AND json_valid(sm.data)
		ORDER BY sm.seq, CAST(c.key AS INTEGER)`, sessionID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("step texts: %w", err)
	}
	defer rows.Close()
	var out []StepText
	for rows.Next() {
		var t StepText
		if err := rows.Scan(&t.MsgID, &t.Index, &t.Seq, &t.Kind, &t.Tool, &t.Text); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
