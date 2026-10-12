package store

import "polaris/threadexport"

// ThreadExportInput gathers everything a thread export needs in one
// consistent pass: the thread's metadata, the effective variant path's
// messages (what the chat view is actually showing, not the root's own
// content when a variant was swapped in), and the thread's full event
// trail.
//
// It resolves through GetThread first, which deliberately excludes
// disabled, hidden-variant and still-ghost threads — so a caller that maps
// sql.ErrNoRows to 404 gets the "ghost threads are refused" behavior from
// docs/plans/thread-export.md for free, with no separate ghost check here.
//
// The messages come from the effective thread id, but the metadata is the
// root's: a fork carries the shared prefix's content, not the thread the
// user is looking at, so its blank title and fork bookkeeping are not what
// an export should show.
func (s *Store) ThreadExportInput(rootID string) (threadexport.Input, error) {
	th, err := s.GetThread(rootID)
	if err != nil {
		return threadexport.Input{}, err
	}
	effective, err := s.EffectiveThreadID(rootID)
	if err != nil {
		return threadexport.Input{}, err
	}
	msgs, err := s.GetMessages(effective)
	if err != nil {
		return threadexport.Input{}, err
	}
	events, err := s.AllEventsForThread(effective)
	if err != nil {
		return threadexport.Input{}, err
	}

	in := threadexport.Input{
		Thread: threadexport.Thread{
			ID:        th.ID,
			Title:     th.Title,
			CreatedAt: th.CreatedAt,
			UpdatedAt: th.UpdatedAt,
			Model:     th.Model,
			Source:    th.Source,
		},
		CompactedSummary:   th.CompactedSummary,
		CompactedThroughID: th.CompactedThroughID,
		Messages:           make([]threadexport.Message, 0, len(msgs)),
		Events:             make([]threadexport.Event, 0, len(events)),
	}
	for _, m := range msgs {
		in.Messages = append(in.Messages, threadexport.Message{
			ID:           m.ID,
			Role:         m.Role,
			Content:      m.Content,
			Citations:    m.Citations,
			Attachments:  m.Attachments,
			CostUSD:      m.CostUSD,
			DurationMs:   m.DurationMs,
			TurnID:       m.TurnID,
			CreatedAt:    m.CreatedAt,
			AppliedModel: m.AppliedModel,
			Transcript:   m.Transcript,
		})
	}
	for _, e := range events {
		in.Events = append(in.Events, threadexport.Event{
			TurnID:    e.TurnID,
			Source:    e.Source,
			Message:   e.Message,
			Data:      e.Data,
			CreatedAt: e.CreatedAt,
		})
	}
	return in, nil
}
