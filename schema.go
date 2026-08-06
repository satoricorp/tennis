package tennis

// The whole database is three tables, one FTS index, and three triggers.
//
// Namespaces are a column rather than a table-name prefix. Prefixing would mean
// building DDL by string concatenation on every call, which is both an
// injection surface and a migration headache; a column keeps every statement
// parameterized and lets one prepared query serve every namespace.
//
// Documents and chunks are separate because a document is what you store and a
// chunk is what gets ranked. Static embedders have no context window to
// overflow, but one vector averaged over ten pages still says very little about
// any of them, so chunking is about ranking quality rather than model limits.
//
// These are slices rather than one blob because CREATE TRIGGER bodies contain
// semicolons, and splitting a blob on ";" mangles them in a way that only shows
// up as a half-created schema at runtime.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS namespaces (
		name        TEXT PRIMARY KEY,
		embedder_id TEXT NOT NULL,
		dims        INTEGER NOT NULL,
		chunk_size  INTEGER NOT NULL,
		chunk_over  INTEGER NOT NULL,
		created_at  TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS docs (
		ns    TEXT NOT NULL,
		id    TEXT NOT NULL,
		text  TEXT NOT NULL,
		attrs TEXT NOT NULL DEFAULT '{}',
		hash  TEXT NOT NULL,
		PRIMARY KEY (ns, id)
	) WITHOUT ROWID`,

	`CREATE TABLE IF NOT EXISTS chunks (
		id     INTEGER PRIMARY KEY,
		ns     TEXT NOT NULL,
		doc_id TEXT NOT NULL,
		ord    INTEGER NOT NULL,
		text   TEXT NOT NULL,
		vec    BLOB NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS chunks_by_doc ON chunks(ns, doc_id)`,

	// External-content FTS5: the index holds only the inverted terms and reads
	// the text back from chunks, so document bodies are not stored twice.
	// Porter stemming means "running" finds "run", which is the behaviour
	// people expect from a search box.
	`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
		text,
		content='chunks',
		content_rowid='id',
		tokenize='porter unicode61'
	)`,
}

// External-content FTS5 tables do NOT follow their source table on their own.
// Without these triggers the index silently drifts on every update and delete:
// stale rows keep matching, deleted documents keep being returned, and nothing
// errors. This is the most common way an FTS5 index rots, so the triggers exist
// in the first migration rather than being bolted on after someone notices.
var triggerStatements = []string{
	`CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
		INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
	END`,

	`CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
	END`,

	`CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
		INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES ('delete', old.id, old.text);
		INSERT INTO chunks_fts(rowid, text) VALUES (new.id, new.text);
	END`,
}

// pragmaStatements are applied per connection.
//
// WAL lets a query run while an index is being written, which matters because
// seeding a large corpus is slow and search should stay available during it.
var pragmaStatements = []string{
	`PRAGMA journal_mode = WAL`,
	`PRAGMA synchronous = NORMAL`,
	`PRAGMA foreign_keys = ON`,
	`PRAGMA busy_timeout = 5000`,
}
