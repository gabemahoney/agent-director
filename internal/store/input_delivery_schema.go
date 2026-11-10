package store

// Shared by fresh creation and the administrator import. Registration is kept
// separate: creating these tables alone must never switch input authority.
const inputDeliveryDDL = `
CREATE TABLE IF NOT EXISTS input_launches (
    store_id TEXT NOT NULL,
    launch_token TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK(revision > 0),
    current_request_id TEXT,
    record TEXT NOT NULL,
    PRIMARY KEY(store_id, launch_token),
    FOREIGN KEY(store_id, launch_token, current_request_id)
      REFERENCES input_attempts(store_id, launch_token, request_id)
      DEFERRABLE INITIALLY DEFERRED
);
CREATE TABLE IF NOT EXISTS input_attempts (
    store_id TEXT NOT NULL,
    launch_token TEXT NOT NULL,
    request_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK(revision > 0),
    record TEXT NOT NULL,
    proof_raw BLOB,
    PRIMARY KEY(store_id, launch_token, request_id),
    FOREIGN KEY(store_id, launch_token) REFERENCES input_launches(store_id, launch_token)
      DEFERRABLE INITIALLY DEFERRED
);
CREATE TABLE IF NOT EXISTS input_imports (
    source TEXT PRIMARY KEY NOT NULL,
    store_id TEXT NOT NULL,
    source_sha256 TEXT NOT NULL,
    evidence BLOB NOT NULL
);
`

// This marker is committed with fresh DDL or the complete administrator import.
// Its presence is checked on the same connection as each input operation. A
// missing marker is corruption, never a capability miss or permission to import.
const inputAuthorityKey = "native_input_authority"
const inputAuthorityVersion = "sqlite-v1:"
