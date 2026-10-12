// Package trace turns a session's messages into an ordered, hierarchical list
// of rows describing what happened. It performs no I/O beyond the loader.
package trace

import (
	"context"

	"github.com/0xGurg/himay/internal/decode"
	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/store"
)

// PageSize is the number of messages fetched per page.
const PageSize = 500

// LoadPage fetches up to limit messages after afterSeq, with assistant content
// summaries attached. The bool reports whether more messages may follow.
func LoadPage(ctx context.Context, st *store.Store, sessionID string, afterSeq int64, limit int) ([]model.Message, bool, error) {
	raws, err := st.Messages(ctx, sessionID, afterSeq, limit)
	if err != nil {
		return nil, false, err
	}
	msgs := make([]model.Message, len(raws))
	for i, r := range raws {
		msgs[i] = decode.Message(r)
	}
	if err := attachItems(ctx, st, sessionID, msgs); err != nil {
		return nil, false, err
	}
	return msgs, len(raws) == limit, nil
}

// Reload re-reads specific messages by ID (for rows rewritten while streaming).
func Reload(ctx context.Context, st *store.Store, sessionID string, ids []string) ([]model.Message, error) {
	raws, err := st.MessagesByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	msgs := make([]model.Message, len(raws))
	for i, r := range raws {
		msgs[i] = decode.Message(r)
	}
	return msgs, attachItems(ctx, st, sessionID, msgs)
}

// LoadAll pages through an entire session. Used by `dump` and provenance.
func LoadAll(ctx context.Context, st *store.Store, sessionID string) ([]model.Message, error) {
	var all []model.Message
	var after int64 = -1
	for {
		page, more, err := LoadPage(ctx, st, sessionID, after, PageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if !more || len(page) == 0 {
			return all, nil
		}
		after = page[len(page)-1].Seq
	}
}

func attachItems(ctx context.Context, st *store.Store, sessionID string, msgs []model.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	idx := map[string]int{}
	var lo, hi int64 = msgs[0].Seq, msgs[0].Seq
	for i, m := range msgs {
		if m.Type == "assistant" {
			idx[m.ID] = i
		}
		lo, hi = min(lo, m.Seq), max(hi, m.Seq)
	}
	if len(idx) == 0 {
		return nil
	}
	items, err := st.Items(ctx, sessionID, lo, hi)
	if err != nil {
		return err
	}
	for _, r := range items {
		if i, ok := idx[r.MsgID]; ok {
			msgs[i].Items = append(msgs[i].Items, decode.Item(r))
		}
	}
	return nil
}
