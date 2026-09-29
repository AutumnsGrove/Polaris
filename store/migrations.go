package store

// migrations adds columns to a threads table created before they existed.
// CREATE TABLE IF NOT EXISTS above only helps brand-new databases — an
// existing polaris.db needs these added explicitly. Applied in order,
// tracked via PRAGMA user_version (see applyMigrations) rather than by
// probing each one's error — append new entries here; never edit or
// reorder existing ones, since a database's recorded version is just an
// index into this slice.
var migrations = []string{
	`ALTER TABLE threads ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN compacted_summary TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE threads ADD COLUMN compacted_through_id INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN suggestions TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE threads ADD COLUMN source TEXT NOT NULL DEFAULT 'web'`,
	`ALTER TABLE messages ADD COLUMN turn_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE events ADD COLUMN turn_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE messages ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN attachment_filename TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE messages ADD COLUMN attachment_content_type TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE threads ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN fork_root_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE threads ADD COLUMN fork_at_index INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN active_variant_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE messages ADD COLUMN cards TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE threads ADD COLUMN continued_in_assistant INTEGER NOT NULL DEFAULT 0`,
	// messages_fts (schema, above) only indexes rows inserted/updated after
	// it existed — a database with messages predating this migration needs
	// a one-time backfill. Plain INSERT, not a "not already indexed" guard:
	// confirmed live that a bare `SELECT rowid FROM messages_fts` on an
	// external-content FTS5 table doesn't read the index at all, it
	// transparently proxies through to the content table (messages) — so
	// that check always found every row "already indexed" and silently
	// backfilled nothing. Safe as a plain INSERT because, like every other
	// entry in this list, it only ever runs once per database via
	// user_version tracking (see applyMigrations); it is not safe to
	// replay by hand.
	`INSERT INTO messages_fts(rowid, content) SELECT id, content FROM messages`,
	`ALTER TABLE messages ADD COLUMN pending_question TEXT NOT NULL DEFAULT ''`,
	// memories shipped before the disabled column existed — an install
	// that already ran CREATE TABLE IF NOT EXISTS memories (above) without
	// it needs this added explicitly, same as every other column here.
	`ALTER TABLE memories ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`,
	// Pulsar's turn-config-persistence prerequisite — see the schema
	// comment above focus_mode. An existing thread gets focus off/research
	// off by default (the same values a fresh composer already starts
	// with), not whatever that thread's last turn actually used, since
	// nothing recorded that until now.
	`ALTER TABLE threads ADD COLUMN focus_mode TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE threads ADD COLUMN deep_research INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN pulsar_routine_id INTEGER`,
	`ALTER TABLE threads ADD COLUMN seen INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN chart TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE pulsar_daily_config ADD COLUMN custom_instructions TEXT NOT NULL DEFAULT '{}'`,
	`ALTER TABLE pulsar_daily_editions ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE pulsar_daily_config ADD COLUMN custom_blocks TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE pulsar_daily_config ADD COLUMN weather_location TEXT NOT NULL DEFAULT ''`,
	// The fourth sticky field, added later than the model/focus_mode/
	// deep_research trio — see the schema comment on no_research. An
	// existing thread defaults to research-on (0), the same value a fresh
	// composer already starts with. Appended at the end, not inserted
	// alongside the other three above — applyMigrations tracks progress by
	// positional index (PRAGMA user_version), not migration content, so
	// inserting mid-list shifts every later index and leaves entries
	// silently unapplied on a database that already ran past that point.
	// Confirmed live: this was originally inserted mid-list and the
	// potato's existing DB skipped it entirely, surfacing as "no such
	// column: no_research" the moment a thread was reopened.
	`ALTER TABLE threads ADD COLUMN no_research INTEGER NOT NULL DEFAULT 0`,
	// Re-attempt, appended fresh: the entry above ran once already as part
	// of the broken mid-list deploy — on a database that already executed
	// it (silently, as a tolerated "duplicate column" skip against
	// whatever migration actually ended up at that shifted index), its
	// user_version has already moved past that slot, so the entry above
	// alone will never run again there. This one gives it an actually-new
	// slot to run in for real. Safe everywhere else too: a fresh database
	// already has the column from CREATE TABLE, so both this and the
	// entry above just hit the same tolerated duplicate-column skip.
	`ALTER TABLE threads ADD COLUMN no_research INTEGER NOT NULL DEFAULT 0`,
	// occurred_at — see the schema comment above. Appended at the end per
	// this file's own established rule: applyMigrations tracks progress by
	// positional index, so a new column always goes last, never inserted
	// alongside the schema comment it corresponds to.
	`ALTER TABLE memories ADD COLUMN occurred_at TEXT NOT NULL DEFAULT ''`,
	// enabled: the Daily-wide on/off switch — see the schema comment above
	// enabled_blocks. Defaults to 1 (on) so an existing install's edition
	// keeps generating on upgrade exactly as it did before this column
	// existed, rather than silently going dark until someone opens
	// settings and notices.
	`ALTER TABLE pulsar_daily_config ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`,
	// backfill_started_at — see the schema comment above. Appended at the
	// end per this file's own established rule (positional user_version
	// tracking, never insert mid-list).
	`ALTER TABLE constellation_config ADD COLUMN backfill_started_at DATETIME`,
	// content_updated_at — see the schema comment above. SQLite's ALTER
	// TABLE ADD COLUMN, unlike CREATE TABLE, rejects a NOT NULL column
	// whose default isn't a real constant — CURRENT_TIMESTAMP doesn't
	// qualify, so a single-statement version of this (as originally
	// written) fails with "Cannot add a column with non-constant default"
	// on every real, populated database, confirmed live against the
	// potato. Add with a constant placeholder default first, then
	// backfill — existing stars all get "now" rather than backdated to
	// their own updated_at, which is fine, GetConstellationWeekFeed only
	// cares about content changes going forward, not reclassifying
	// history. On a fresh database the column already exists (added by
	// CREATE TABLE, where this restriction doesn't apply), so the ALTER
	// hits applyMigrations' tolerated "duplicate column" skip and the
	// UPDATE never runs there — harmless, since CREATE TABLE's own
	// default already populated it correctly.
	`ALTER TABLE stars ADD COLUMN content_updated_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00'; UPDATE stars SET content_updated_at = CURRENT_TIMESTAMP`,
	// workspace_file_id — see the schema comment above. Appended at the
	// end per this file's own established rule (positional user_version
	// tracking, never insert mid-list).
	`ALTER TABLE messages ADD COLUMN workspace_file_id TEXT NOT NULL DEFAULT ''`,
	// attachments — see the schema comment above (issue #71, multiple
	// attachments per turn). The three singular attachment_* columns
	// above are now frozen: no code writes them again after this
	// migration ships. GetMessages synthesizes a one-element attachments
	// array from them for any row written before this column existed,
	// so old messages still display correctly without a backfill.
	`ALTER TABLE messages ADD COLUMN attachments TEXT NOT NULL DEFAULT '[]'`,
	// person_name/person_pronouns — see the schema comment above. Appended
	// at the end per this file's own established rule (positional
	// user_version tracking, never insert mid-list).
	`ALTER TABLE constellation_config ADD COLUMN person_name TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE constellation_config ADD COLUMN person_pronouns TEXT NOT NULL DEFAULT ''`,
	// person_name/person_pronouns move from being Constellation-owned (the
	// two migrations above) to the general operator-level settings table,
	// so the main assistant's own system prompt can use them too, not just
	// Weaver's — see gateway/settings.go's settingPersonName/
	// settingPersonPronouns. One-time copy of whatever was already saved;
	// constellation_config's own columns are left in place (unused from
	// here on) rather than dropped, matching this file's usual "don't claw
	// back schema" convention for a superseded column.
	`INSERT OR IGNORE INTO settings (key, value) SELECT 'person_name', person_name FROM constellation_config WHERE id = 1 AND person_name != ''`,
	`INSERT OR IGNORE INTO settings (key, value) SELECT 'person_pronouns', person_pronouns FROM constellation_config WHERE id = 1 AND person_pronouns != ''`,
	// tts_audio_file_id — see the schema comment above. Appended at the
	// end per this file's own established rule (positional user_version
	// tracking, never insert mid-list).
	`ALTER TABLE messages ADD COLUMN tts_audio_file_id TEXT NOT NULL DEFAULT ''`,
	// used_transponder — plain informational flag, same shape as favorite:
	// set true the first time a turn on this thread carries voice_mode
	// (see gateway/turn.go, docs/plans/transponder.md's "Resolved" section
	// on why this isn't Source instead — a thread can move freely between
	// chat and call view, so this has to be settable on any later turn,
	// not just fixed at creation).
	`ALTER TABLE threads ADD COLUMN used_transponder INTEGER NOT NULL DEFAULT 0`,
	// deepseek-pro was retired from models/models.go's registry
	// (2026-09-22, see that file's comment) — repoint any already-stored
	// Pulsar Daily config still pointing at the dead ID to "deepseek"
	// (V4.1 Flash), the same model CREATE TABLE's own architect_model
	// default now uses for fresh installs. Without this, ModelByID would
	// have silently fallen back to whatever cfg.DefaultModel happens to
	// be — which is "deepseek" today, but only by coincidence, not by
	// anything this row actually says.
	`UPDATE pulsar_daily_config SET architect_model = 'deepseek' WHERE architect_model = 'deepseek-pro'`,
	// verification — see the schema comment above. Appended at the end
	// per this file's own established rule (positional user_version
	// tracking, never insert mid-list).
	`ALTER TABLE messages ADD COLUMN verification TEXT NOT NULL DEFAULT '[]'`,
	// prompt_tokens/cache_read_tokens — see the schema comment above.
	`ALTER TABLE messages ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0`,
	// transcript — see the schema comment above.
	`ALTER TABLE messages ADD COLUMN transcript TEXT NOT NULL DEFAULT ''`,
	// compacted_pending_notice/compacted_pending_cost — see the schema
	// comment above. Appended at the end per this file's own established
	// rule (positional user_version tracking, never insert mid-list).
	// An existing thread defaults to "nothing pending", which is right: it
	// may well have compacted under the old synchronous scheme, but that
	// compaction already announced itself live, and re-announcing it on the
	// next turn would show a summary the user has already seen.
	`ALTER TABLE threads ADD COLUMN compacted_pending_notice INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE threads ADD COLUMN compacted_pending_cost REAL NOT NULL DEFAULT 0`,
	// ghost — see the schema comment above. Appended at the end per this
	// file's own established rule (positional user_version tracking, never
	// insert mid-list). An existing thread defaults to "not ghost", which is
	// right: this flag only ever matters for a brand-new thread's creation
	// turn going forward.
	`ALTER TABLE threads ADD COLUMN ghost INTEGER NOT NULL DEFAULT 0`,
	// oracle_result/focus_mode_source/cost_answer_usd/cost_verification_usd/
	// cost_oracle_usd/ttft_ms/tokens_per_second/tool_call_count — see the
	// schema comments above. Appended at the end per this file's own
	// established rule (positional user_version tracking, never insert
	// mid-list). Every existing row's cost_answer_usd starts at 0 rather
	// than being backfilled from its existing cost_usd — there's no way to
	// know in hindsight how an old turn's total split across tiers, and a
	// stats display reading these new columns only needs to be accurate
	// going forward.
	`ALTER TABLE messages ADD COLUMN oracle_result TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE messages ADD COLUMN focus_mode_source TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE messages ADD COLUMN cost_answer_usd REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN cost_verification_usd REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN cost_oracle_usd REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN ttft_ms INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN tokens_per_second REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE messages ADD COLUMN tool_call_count INTEGER NOT NULL DEFAULT 0`,
	// applied_focus_mode — see the schema comment above. Appended at the
	// end per this file's own established rule (positional user_version
	// tracking, never insert mid-list). An existing row defaults to ""
	// (no way to know in hindsight what an old turn actually ran with),
	// which just means its own margin note (were Oracle mode ever
	// retroactively read from it) renders no focus clause — a normal,
	// silent "nothing to say" outcome, not a wrong one.
	`ALTER TABLE messages ADD COLUMN applied_focus_mode TEXT NOT NULL DEFAULT ''`,
	// applied_model — see the schema comment above. Same reasoning/
	// appended-at-the-end placement as applied_focus_mode just above.
	`ALTER TABLE messages ADD COLUMN applied_model TEXT NOT NULL DEFAULT ''`,
	// completion_tokens — see the schema comment above.
	`ALTER TABLE messages ADD COLUMN completion_tokens INTEGER NOT NULL DEFAULT 0`,
	// jev_usage.source — see the schema comment above (issue #125).
	`ALTER TABLE jev_usage ADD COLUMN source TEXT NOT NULL DEFAULT ''`,
	// threads.field_id — see the schema comment above (issue #119).
	// Appended at the end per this file's own established rule (positional
	// user_version tracking, never insert mid-list). NULL default is also
	// what SQLite requires to ADD COLUMN ... REFERENCES at all.
	`ALTER TABLE threads ADD COLUMN field_id TEXT REFERENCES fields(id)`,
}
