package gateway

import (
	"polaris/store"
)

// historyEntries is the thread/message read behind loadHistory and
// loadAnswerHistory (see history_replay.go) — split out so they can reach
// each entry's TurnID/Transcript, which llm.ChatMessage has no field for.
// excludeFromID is passed straight through to store.EffectiveHistory.
func (s *Server) historyEntries(threadID string, excludeFromID int64) ([]store.HistoryEntry, error) {
	// GetThreadRaw, not the public GetThread — threadID here is always
	// storageThreadID, which is legitimately a hidden fork's own id for
	// an edit/retry turn, and the public GetThread now deliberately
	// rejects those (see its doc comment).
	thread, err := s.db.GetThreadRaw(threadID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.db.GetMessages(threadID)
	if err != nil {
		return nil, err
	}

	return store.EffectiveHistory(thread, msgs, excludeFromID), nil
}
