package store

// migrations are applied in order; never edit a released entry, append a new one.
var migrations = []string{
	// 1: baseline
	`
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE users (
	id            INTEGER PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	email         TEXT UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	role          TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('member','vip','admin')),
	vip_until     TEXT,
	disabled      INTEGER NOT NULL DEFAULT 0,
	invited_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
	invite_id     INTEGER,
	created_at    TEXT NOT NULL,
	last_login_at TEXT
);

CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	ip         TEXT,
	user_agent TEXT
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE invites (
	id         INTEGER PRIMARY KEY,
	code       TEXT NOT NULL UNIQUE,
	created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	max_uses   INTEGER NOT NULL DEFAULT 1,   -- 0 = unlimited
	uses       INTEGER NOT NULL DEFAULT 0,
	role       TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('member','vip')),
	vip_days   INTEGER NOT NULL DEFAULT 0,
	expires_at TEXT,
	note       TEXT NOT NULL DEFAULT '',
	disabled   INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);

CREATE TABLE redeem_codes (
	id         INTEGER PRIMARY KEY,
	code       TEXT NOT NULL UNIQUE,
	vip_days   INTEGER NOT NULL,
	note       TEXT NOT NULL DEFAULT '',
	created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	created_at TEXT NOT NULL,
	used_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
	used_at    TEXT
);

CREATE TABLE qb_servers (
	id                     INTEGER PRIMARY KEY,
	name                   TEXT NOT NULL UNIQUE,
	url                    TEXT NOT NULL,
	username               TEXT NOT NULL DEFAULT '',
	password               TEXT NOT NULL DEFAULT '',
	api_key                TEXT NOT NULL DEFAULT '',
	category               TEXT NOT NULL DEFAULT 'projectk',
	enabled                INTEGER NOT NULL DEFAULT 1,
	seeding_time_limit_min INTEGER NOT NULL DEFAULT 1440,
	ratio_limit            REAL NOT NULL DEFAULT -2,  -- -2 = use global, -1 = unlimited
	add_wss_trackers       INTEGER NOT NULL DEFAULT 1,
	created_at             TEXT NOT NULL
);

-- Policy rules. The most specific/high priority match wins; see internal/policy.
CREATE TABLE rules (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	enabled    INTEGER NOT NULL DEFAULT 1,
	priority   INTEGER NOT NULL DEFAULT 0,
	field      TEXT NOT NULL CHECK (field IN ('title','file','infohash','team','tag','any')),
	op         TEXT NOT NULL CHECK (op IN ('contains','regex','equals')),
	value      TEXT NOT NULL,
	action     TEXT NOT NULL CHECK (action IN ('allow','login','vip','block')),
	label      TEXT NOT NULL DEFAULT '',
	note       TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

-- Per-torrent manual decisions (highest precedence).
CREATE TABLE verdicts (
	infohash   TEXT PRIMARY KEY,
	action     TEXT NOT NULL CHECK (action IN ('allow','login','vip','block')),
	label      TEXT NOT NULL DEFAULT '',
	note       TEXT NOT NULL DEFAULT '',
	decided_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	decided_at TEXT NOT NULL
);

CREATE TABLE reviews (
	id          INTEGER PRIMARY KEY,
	infohash    TEXT NOT NULL,
	title       TEXT NOT NULL DEFAULT '',
	source      TEXT NOT NULL CHECK (source IN ('report','auto')),
	reason      TEXT NOT NULL DEFAULT '',
	reporter_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
	status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','resolved','dismissed')),
	created_at  TEXT NOT NULL,
	resolved_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
	resolved_at TEXT
);
CREATE INDEX reviews_status ON reviews(status, id);
CREATE UNIQUE INDEX reviews_pending_once ON reviews(infohash, source) WHERE status = 'pending';

-- Torrents we have seen/played (cache of what policy looked at).
CREATE TABLE torrents (
	infohash    TEXT PRIMARY KEY,
	title       TEXT NOT NULL DEFAULT '',
	size_bytes  INTEGER NOT NULL DEFAULT 0,
	files_json  TEXT NOT NULL DEFAULT '[]',
	team        TEXT NOT NULL DEFAULT '',
	tags_json   TEXT NOT NULL DEFAULT '[]',
	source      TEXT NOT NULL DEFAULT 'user',
	source_id   TEXT NOT NULL DEFAULT '',
	first_seen  TEXT NOT NULL,
	last_played TEXT
);

CREATE TABLE plays (
	id         INTEGER PRIMARY KEY,
	infohash   TEXT NOT NULL,
	title      TEXT NOT NULL DEFAULT '',
	user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
	ip         TEXT NOT NULL DEFAULT '',
	action     TEXT NOT NULL,
	seeded_by  TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX plays_time ON plays(created_at);

CREATE TABLE audit (
	id         INTEGER PRIMARY KEY,
	actor_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
	action     TEXT NOT NULL,
	target     TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL DEFAULT '',
	ip         TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX audit_time ON audit(id DESC);
`,
	// 2: user-supplied magnet names are kept apart from trusted titles (qB metadata / catalogue);
	// indexes for retention pruning.
	`
ALTER TABLE torrents ADD COLUMN user_title TEXT NOT NULL DEFAULT '';
CREATE INDEX audit_created ON audit(created_at);
CREATE INDEX reviews_resolved ON reviews(resolved_at) WHERE status != 'pending';
`,
	// 3: per-route permissions (admin panel "接口权限"). route_seeds remembers which routes
	// already received their default grants so later admin removals stick.
	`
CREATE TABLE role_permissions (
	role  TEXT NOT NULL CHECK (role IN ('guest','member','vip')),
	route TEXT NOT NULL,
	PRIMARY KEY (role, route)
);
CREATE TABLE route_seeds (
	route     TEXT PRIMARY KEY,
	seeded_at TEXT NOT NULL
);
`,
}
