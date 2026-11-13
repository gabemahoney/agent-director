package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// The busy-send permanent core (SR-2.1, SR-7.2): schema v21's tables, their
// indexes, three spawns columns and one store_meta identity row. The exempt
// install's v20→v21 hop (migrateV20toV21) is the only conversion the core ever
// gets: every later version reads and mutates these records directly and
// recognizes them at open (recognizeBusySendCore) without migrating. So the
// declarations below are permanent text: open compares each one, whitespace
// aside, with the store's own, and a later version can neither edit nor drop
// one. Their documentation lives in these Go comments, never in SQL comments,
// so documenting a column never changes the contract.
//
// Conventions for every core table:
//   - store_id is the store's store_meta store_id; every row names it, so no
//     record can be read as another store's.
//   - instance_id is the record's original target. No core table has a
//     foreign key to spawns, so deleting or expiring an agent row removes no
//     custody, Result, pair ownership, force or pause claim (SR-6.3).
//   - *_ms columns are milliseconds since the epoch; NULL is "not observed" or
//     "not recorded", never a guess, and no writer sets one from elapsed time.
//   - revision is the entity's revision: each mutation compares the revision
//     (and owner) it captured and advances it, with its dependent records, in
//     one transaction (SR-7.2). The table comment names each entity's owner.
//   - Coded columns hold busy_send_records.go's string codes. A CHECK lists
//     the codes of each closed set; reason columns have none, so their
//     catalogue can grow without a conversion.
//   - A CHECK passes when it evaluates to NULL, so a rule's consequent never
//     compares a nullable column with =, IN or <> alone: it uses IS, IS NOT
//     NULL or an explicit NULL test, and a nullable integer is typeof-checked
//     like a NOT NULL one. A nullable column's own code list still admits
//     NULL, which is "absent".
//   - Foreign keys among core tables are DEFERRABLE INITIALLY DEFERRED, so a
//     writer may insert a record and its references in any order within its
//     transaction; none cascades.

// busy_conversations is the conversation inbox (SR-2.1 "Conversation inbox";
// M1's lane), one row per conversation of an instance:
//   - conversation_id: opaque AD-made identity. A resumed Launch keeps it; a
//     fresh reuse creates a new one even when life_number does not change.
//   - generation: the instance's conversation generation, 1 for its first,
//     independent of life_number and of the provider session.
//   - publication: BusySendPublication. One published and at most one
//     candidate per instance (the two partial unique indexes); a candidate is
//     a fresh reuse's conversation, kept apart while its creation is pending
//     or uncertain, so admissions keep binding to the published one (SR-6.3).
//   - provider: the published provider (the agent kind of the Launch that
//     published it); it never changes after publication.
//   - busy_send: the conversation's coverage (SR-9.1). It never decreases:
//     covered conversations stay covered across every resume.
//   - default_send_mode, default_confirmed: the Agent Defaults published with
//     the conversation (SR-1.4); confirm_from_transcripts: SR-4.5's setting.
//   - accepted_seq: the last accepted sequence allocated; the next admission
//     takes accepted_seq+1 in the same transaction (SR-1.1).
//   - reserved_count, reserved_bytes: SR-1.6's held reservations, the count
//     and text bytes of accepted custody that can still return to Held;
//     claiming or issuing a prompt does not free them.
//   - owner_handover_id: the current hand-over owner, the one open hand-over
//     that owns order and transport (SR-2.2, SR-2.5); NULL when none.
//   - handover_force_id: the most recent force that began a hand-over
//     (SR-5.6), so a later force refused before any hand-over never replaces it.
//   - revision: owned by the inbox writers (admission, claim, hand-over
//     ownership, lifecycle publication).
//   - recorded_at_ms, published_at_ms, retired_at_ms, retired_reason,
//     replaced_by: when the core recorded it, published it and retired it,
//     why, and the conversation that replaced it.
const busyConversationsDDL = `CREATE TABLE IF NOT EXISTS busy_conversations (
    conversation_id          TEXT PRIMARY KEY NOT NULL,
    store_id                 TEXT NOT NULL,
    instance_id              TEXT NOT NULL,
    generation               INTEGER NOT NULL CHECK(typeof(generation) = 'integer' AND generation > 0),
    publication              TEXT NOT NULL CHECK(publication IN ('candidate', 'published', 'retired', 'abandoned')),
    provider                 TEXT NOT NULL CHECK(provider <> ''),
    busy_send                TEXT NOT NULL DEFAULT 'not_covered' CHECK(busy_send IN ('not_covered', 'covered')),
    default_send_mode        TEXT NOT NULL DEFAULT 'wait_turn' CHECK(default_send_mode IN ('wait_turn', 'steer_turn')),
    default_confirmed        INTEGER NOT NULL DEFAULT 0 CHECK(default_confirmed IN (0, 1)),
    confirm_from_transcripts INTEGER NOT NULL DEFAULT 0 CHECK(confirm_from_transcripts IN (0, 1)),
    accepted_seq             INTEGER NOT NULL DEFAULT 0 CHECK(typeof(accepted_seq) = 'integer' AND accepted_seq >= 0),
    reserved_count           INTEGER NOT NULL DEFAULT 0 CHECK(typeof(reserved_count) = 'integer' AND reserved_count >= 0),
    reserved_bytes           INTEGER NOT NULL DEFAULT 0 CHECK(typeof(reserved_bytes) = 'integer' AND reserved_bytes >= 0),
    owner_handover_id        TEXT,
    handover_force_id        TEXT,
    revision                 INTEGER NOT NULL DEFAULT 1 CHECK(typeof(revision) = 'integer' AND revision > 0),
    recorded_at_ms           INTEGER NOT NULL,
    published_at_ms          INTEGER,
    retired_at_ms            INTEGER,
    retired_reason           TEXT CHECK(retired_reason IN ('conversation_replaced', 'record_deleted')),
    replaced_by              TEXT,
    UNIQUE(instance_id, generation),
    CHECK((publication = 'retired') = (retired_reason IS NOT NULL)),
    CHECK((retired_at_ms IS NULL) = (retired_reason IS NULL)),
    CHECK((publication IN ('published', 'retired')) = (published_at_ms IS NOT NULL)),
    CHECK(replaced_by IS NULL OR retired_reason IS 'conversation_replaced'),
    FOREIGN KEY(owner_handover_id) REFERENCES busy_send_handovers(handover_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(handover_force_id) REFERENCES busy_send_forces(force_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(replaced_by) REFERENCES busy_conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_launches holds one row per Launch the core knows (SR-7.2's
// per-Launch facts; M1's lane launch and life operations):
//   - life_number, launch_token: the spawns row's life and launch token the
//     Launch began with; launch_token is NULL when none was recorded.
//   - conversation_id: the conversation the Launch serves. A resume's
//     Launch serves the published conversation; a fresh reuse's serves its
//     candidate until publication.
//   - provider, busy_send: the Launch's agent kind and its coverage, fixed
//     when the Launch is created from the launching version's covered set
//     (SR-9.1) and kept until it ends. spawns.busy_send repeats the current
//     Launch's value for the row's readers.
//   - kind, phase: BusySendLaunchKind, BusySendLaunchPhase. One current and at
//     most one candidate or uncertain Launch per instance. The candidate
//     Launch and its external creation outcome stay apart from the published
//     conversation and current Launch (SR-6.3).
//   - route_kind, route_id, connection_generation: the provider-neutral route
//     identity and connection generation (SR-3.6, SR-7.2); a reconnect
//     advances the generation, fencing work captured on an older one.
//   - wake_path: the Launch's one content-free wake endpoint, a file whose
//     change notifies its route (SR-7.2). It carries no Message authority.
//   - revision: owned by the lifecycle writers (spawn, reuse, resume,
//     Launch End) and the route's connection writer.
//   - ended_at_ms, end_cause: the Launch End and its evidence
//     (BusySendLaunchEnd; SR-6.2), recorded once.
const busySendLaunchesDDL = `CREATE TABLE IF NOT EXISTS busy_send_launches (
    launch_id             TEXT PRIMARY KEY NOT NULL,
    store_id              TEXT NOT NULL,
    instance_id           TEXT NOT NULL,
    conversation_id       TEXT NOT NULL,
    life_number           INTEGER NOT NULL CHECK(typeof(life_number) = 'integer' AND life_number >= 0),
    launch_token          TEXT,
    provider              TEXT NOT NULL CHECK(provider <> ''),
    busy_send             TEXT NOT NULL CHECK(busy_send IN ('not_covered', 'covered')),
    kind                  TEXT NOT NULL CHECK(kind IN ('spawn', 'reuse', 'resume', 'migrated')),
    phase                 TEXT NOT NULL CHECK(phase IN ('candidate', 'uncertain', 'failed', 'current', 'superseded')),
    route_kind            TEXT CHECK(route_kind IN ('claude_hooks', 'codex_app_server', 'cursor_acp')),
    route_id              TEXT,
    connection_generation INTEGER NOT NULL DEFAULT 0 CHECK(typeof(connection_generation) = 'integer' AND connection_generation >= 0),
    wake_path             TEXT,
    revision              INTEGER NOT NULL DEFAULT 1 CHECK(typeof(revision) = 'integer' AND revision > 0),
    recorded_at_ms        INTEGER NOT NULL,
    ended_at_ms           INTEGER,
    end_cause             TEXT CHECK(end_cause IN ('process_exited', 'launch_published', 'creation_failed')),
    CHECK((ended_at_ms IS NULL) = (end_cause IS NULL)),
    CHECK((phase = 'failed') = (end_cause IS 'creation_failed')),
    CHECK(phase <> 'superseded' OR ended_at_ms IS NOT NULL),
    FOREIGN KEY(conversation_id) REFERENCES busy_conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_launch_processes binds a Launch to the processes its route owns
// (SR-3.6, SR-6.5's "recorded agent process"), one row per role
// (BusySendProcessRole): pid, process start time and PID namespace identify
// the process; endpoint is the role's private endpoint, such as the Codex
// app-server's socket path. A Launch End is established against these
// identities, never against the row's state alone (SR-6.2).
const busySendLaunchProcessesDDL = `CREATE TABLE IF NOT EXISTS busy_send_launch_processes (
    launch_id      TEXT NOT NULL,
    role           TEXT NOT NULL CHECK(role IN ('agent', 'app_server', 'tui', 'helper', 'gateway')),
    store_id       TEXT NOT NULL,
    pid            INTEGER NOT NULL CHECK(typeof(pid) = 'integer' AND pid > 0),
    start_time     TEXT NOT NULL CHECK(start_time <> ''),
    pid_namespace  TEXT,
    endpoint       TEXT,
    recorded_at_ms INTEGER NOT NULL,
    PRIMARY KEY(launch_id, role),
    FOREIGN KEY(launch_id) REFERENCES busy_send_launches(launch_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_messages holds every accepted Message, confirmed or not
// (SR-2.1 "Message"; M1's msg):
//   - message_id: internal identity, also of unconfirmed Messages, never
//     exposed for one (SR-1.3). A confirmed Message's pair is its
//     busy_send_results row.
//   - conversation_id, instance_id, accepted_seq, accepted_at_ms: the
//     immutable target, its conversation and the acceptance order and time.
//   - send_mode, confirmed: the resolved options (SR-1.4), immutable.
//   - text, text_bytes, text_sha256: the validated text bytes, never
//     normalized, their length and SHA-256 (lowercase hex). Immutable while
//     retained; expiry removes text and digest and keeps the length.
//   - place, handover_id: current custody (BusySendPlace) and its nullable
//     current hand-over membership, set exactly while claimed or issued.
//     Every return to Held clears handover_id in the same transaction. Earlier
//     attempts are the Message's busy_send_handover_members rows, each with
//     its hand-over's Launch.
//   - reserved: whether it counts in its conversation's held reservations.
//   - forced: set when a force's hand-over of it actually starts (SR-4.2).
//   - race_exposed: SR-3.3a's sticky boundary-race exposure.
//   - result_class, result_final, result_reason, wait_*: the current Result
//     (SR-5.1), kept apart from custody: a Final Result is never rewritten.
//     wait_earlier_message_id names the earlier confirmed Message it waits on.
//   - bound_launch_id, bound_launch_token, bound_session_id: the actual
//     hand-over binding, NULL until established.
//   - evidence_kind, record_id, record_sha256: the qualified receipt's
//     identity and digest, never raw evidence.
//   - queued_at_ms, handed_over_at_ms, delivered_at_ms, final_at_ms: observed
//     transition times; final_at_ms also starts a confirmed Result's
//     retention (SR-5.5).
//   - retention, expired_at_ms: expiry removes the text, digest, Result and
//     receipt identity and keeps the non-content facts b.7si reads (acceptance
//     time, custody and its hand-over history) and the pair ownership.
//   - revision: owned by the inbox writers (admission, claim, hand-over,
//     force, Launch End and retirement, and expiry).
const busySendMessagesDDL = `CREATE TABLE IF NOT EXISTS busy_send_messages (
    message_id              TEXT PRIMARY KEY NOT NULL,
    store_id                TEXT NOT NULL,
    conversation_id         TEXT NOT NULL,
    instance_id             TEXT NOT NULL,
    accepted_seq            INTEGER NOT NULL CHECK(typeof(accepted_seq) = 'integer' AND accepted_seq > 0),
    accepted_at_ms          INTEGER NOT NULL,
    send_mode               TEXT NOT NULL CHECK(send_mode IN ('wait_turn', 'steer_turn')),
    confirmed               INTEGER NOT NULL CHECK(confirmed IN (0, 1)),
    text                    BLOB,
    text_bytes              INTEGER NOT NULL CHECK(typeof(text_bytes) = 'integer' AND text_bytes > 0),
    text_sha256             TEXT,
    place                   TEXT NOT NULL CHECK(place IN ('held', 'claimed', 'issued', 'closed')),
    handover_id             TEXT,
    reserved                INTEGER NOT NULL CHECK(reserved IN (0, 1)),
    forced                  INTEGER NOT NULL DEFAULT 0 CHECK(forced IN (0, 1)),
    race_exposed            INTEGER NOT NULL DEFAULT 0 CHECK(race_exposed IN (0, 1)),
    result_class            TEXT CHECK(result_class IN ('held', 'queued', 'delivered_confirmed', 'sent_unconfirmed', 'not_started')),
    result_final            INTEGER NOT NULL DEFAULT 0 CHECK(result_final IN (0, 1)),
    result_reason           TEXT,
    wait_reason             TEXT,
    wait_since_ms           INTEGER,
    wait_earlier_message_id TEXT,
    bound_launch_id         TEXT,
    bound_launch_token      TEXT,
    bound_session_id        TEXT,
    evidence_kind           TEXT CHECK(evidence_kind IN ('codex_item_started', 'codex_thread_items', 'cursor_end_turn')),
    record_id               TEXT,
    record_sha256           TEXT,
    queued_at_ms            INTEGER,
    handed_over_at_ms       INTEGER,
    delivered_at_ms         INTEGER,
    final_at_ms             INTEGER,
    retention               TEXT NOT NULL DEFAULT 'retained' CHECK(retention IN ('retained', 'expired')),
    expired_at_ms           INTEGER,
    revision                INTEGER NOT NULL DEFAULT 1 CHECK(typeof(revision) = 'integer' AND revision > 0),
    UNIQUE(conversation_id, accepted_seq),
    CHECK((handover_id IS NOT NULL) = (place IN ('claimed', 'issued'))),
    CHECK((result_final = 1) = (final_at_ms IS NOT NULL)),
    CHECK((wait_reason IS NULL) = (wait_since_ms IS NULL)),
    CHECK(wait_reason IS NULL OR result_final = 0),
    CHECK(wait_earlier_message_id IS NULL OR wait_reason IS NOT NULL),
    CHECK(record_id IS NULL OR evidence_kind IS NOT NULL),
    CHECK(record_sha256 IS NULL OR evidence_kind IS NOT NULL),
    CHECK((retention = 'expired') = (expired_at_ms IS NOT NULL)),
    CHECK(retention = 'expired' OR (text IS NOT NULL AND text_sha256 IS NOT NULL AND result_class IS NOT NULL)),
    CHECK(retention = 'retained' OR (result_final = 1 AND text IS NULL AND text_sha256 IS NULL AND result_class IS NULL AND result_reason IS NULL AND evidence_kind IS NULL AND record_id IS NULL AND record_sha256 IS NULL)),
    FOREIGN KEY(conversation_id) REFERENCES busy_conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(handover_id) REFERENCES busy_send_handovers(handover_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(wait_earlier_message_id) REFERENCES busy_send_messages(message_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(bound_launch_id) REFERENCES busy_send_launches(launch_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_results is store-wide confirmed pair ownership (SR-2.1 "Retained
// result / tombstone", SR-1.2): one permanent row per (caller_uuid,
// delivery_request_id), naming the owning Message and its target. The Result
// itself and its retention are the Message's columns, so the pair outlives
// the agent row and the Result's expiry: an expired Message keeps the pair
// as a tombstone with no text, Result or raw evidence (SR-5.5). caller_uuid
// is the canonical lowercase hyphenated form. Rows are never updated or
// deleted.
const busySendResultsDDL = `CREATE TABLE IF NOT EXISTS busy_send_results (
    caller_uuid         TEXT NOT NULL CHECK(length(caller_uuid) = 36 AND caller_uuid = lower(caller_uuid)),
    delivery_request_id TEXT NOT NULL CHECK(delivery_request_id <> ''),
    store_id            TEXT NOT NULL,
    message_id          TEXT NOT NULL UNIQUE,
    instance_id         TEXT NOT NULL,
    PRIMARY KEY(caller_uuid, delivery_request_id),
    FOREIGN KEY(message_id) REFERENCES busy_send_messages(message_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_handovers holds every hand-over (SR-2.1 "Hand-over"; M1's batch):
//   - conversation_id, handover_seq: the conversation and its hand-over order
//     (1, 2, ...); the latest one is the highest handover_seq.
//   - instance_id, launch_id, launch_token, provider, session_id, route_kind,
//     route_id, connection_generation: the destination captured at claim.
//     Revalidation compares them with the current ones, and dispatch goes
//     only to them (SR-2.4).
//   - owner_revision: the conversation revision that granted ownership.
//   - revision: owned by the hand-over's job and its fencing callers.
//   - cutoff_seq: the accepted-sequence cutoff that froze its membership.
//   - boundary, race_exposed: BusySendBoundary and SR-3.3a's exposure.
//   - forced, force_id: an explicit force's hand-over and its force.
//   - expected_turn_starts, expected_turn_id, permission_epoch: the turn
//     (the Launch's Turn Start counter, and the provider turn id where the
//     route has one) and permission epochs captured for revalidation.
//   - prompt_count, prompt_cursor: the serialized partition's size and the
//     next prompt to issue; a force's remaining prompt cursor is its
//     hand-over's prompt_cursor.
//   - intent_at_ms, intent_turn_starts, intent_running_turn: the first
//     persisted emission intent, with the spawns row's Turn Start counter and
//     whether the hand-over went into a running turn, all captured in that
//     intent's transaction (b.7si t1.7si.nc SR-1.3, SR-1.4).
//   - state, closed_at_ms: whether it still owns order and transport.
//   - claimed_at_ms, issue_deadline_ms, job_deadline_ms: its finite job's
//     start and original budgets: nothing passes revalidation after the issue
//     deadline, and the job ends by the job deadline (SR-3.2). Neither is
//     ever renewed.
const busySendHandoversDDL = `CREATE TABLE IF NOT EXISTS busy_send_handovers (
    handover_id           TEXT PRIMARY KEY NOT NULL,
    store_id              TEXT NOT NULL,
    conversation_id       TEXT NOT NULL,
    handover_seq          INTEGER NOT NULL CHECK(typeof(handover_seq) = 'integer' AND handover_seq > 0),
    instance_id           TEXT NOT NULL,
    launch_id             TEXT NOT NULL,
    launch_token          TEXT,
    provider              TEXT NOT NULL CHECK(provider <> ''),
    session_id            TEXT,
    route_kind            TEXT NOT NULL CHECK(route_kind IN ('claude_hooks', 'codex_app_server', 'cursor_acp')),
    route_id              TEXT,
    connection_generation INTEGER NOT NULL CHECK(typeof(connection_generation) = 'integer' AND connection_generation >= 0),
    owner_revision        INTEGER NOT NULL CHECK(typeof(owner_revision) = 'integer' AND owner_revision > 0),
    revision              INTEGER NOT NULL DEFAULT 1 CHECK(typeof(revision) = 'integer' AND revision > 0),
    cutoff_seq            INTEGER NOT NULL CHECK(typeof(cutoff_seq) = 'integer' AND cutoff_seq > 0),
    boundary              TEXT NOT NULL CHECK(boundary IN ('turn_end', 'tool_boundary', 'force')),
    race_exposed          INTEGER NOT NULL CHECK(race_exposed IN (0, 1)),
    forced                INTEGER NOT NULL CHECK(forced IN (0, 1)),
    force_id              TEXT,
    expected_turn_starts  INTEGER NOT NULL CHECK(typeof(expected_turn_starts) = 'integer' AND expected_turn_starts >= 0),
    expected_turn_id      TEXT,
    permission_epoch      INTEGER NOT NULL CHECK(typeof(permission_epoch) = 'integer' AND permission_epoch >= 0),
    prompt_count          INTEGER NOT NULL CHECK(typeof(prompt_count) = 'integer' AND prompt_count > 0),
    prompt_cursor         INTEGER NOT NULL DEFAULT 0 CHECK(typeof(prompt_cursor) = 'integer' AND prompt_cursor >= 0 AND prompt_cursor <= prompt_count),
    intent_at_ms          INTEGER,
    intent_turn_starts    INTEGER CHECK(intent_turn_starts IS NULL OR (typeof(intent_turn_starts) = 'integer' AND intent_turn_starts >= 0)),
    intent_running_turn   INTEGER CHECK(intent_running_turn IN (0, 1)),
    state                 TEXT NOT NULL CHECK(state IN ('open', 'closed')),
    claimed_at_ms         INTEGER NOT NULL,
    issue_deadline_ms     INTEGER NOT NULL,
    job_deadline_ms       INTEGER NOT NULL,
    closed_at_ms          INTEGER,
    UNIQUE(conversation_id, handover_seq),
    CHECK(forced = (force_id IS NOT NULL)),
    CHECK((boundary = 'force') = (forced = 1)),
    CHECK((intent_at_ms IS NULL) = (intent_turn_starts IS NULL)),
    CHECK((intent_at_ms IS NULL) = (intent_running_turn IS NULL)),
    CHECK((state = 'closed') = (closed_at_ms IS NOT NULL)),
    CHECK(job_deadline_ms >= issue_deadline_ms AND issue_deadline_ms >= claimed_at_ms),
    FOREIGN KEY(conversation_id) REFERENCES busy_conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(launch_id) REFERENCES busy_send_launches(launch_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(force_id) REFERENCES busy_send_forces(force_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_prompts holds a hand-over's immutable prompt partition (SR-2.3;
// M1's job), one row per prompt in issue order, with its transport facts:
//   - message_count, rendered_bytes: the prompt's whole-Message partition and
//     its complete rendered size, separators and wrappers included.
//   - route_request_id, client_id: the route's request and client identities
//     for this prompt, correlation only, never provider idempotency.
//   - emission and its times: BusySendEmission (SR-2.4). The intent and the
//     captured destination commit before any irreversible route call.
//   - transport, order_kind, admission_kind, negative_kind and their times:
//     observed transport, order, admission and definitive negative evidence,
//     each independent (SR-2.5, SR-4.3). A transport write is never a
//     receipt or an admission, and none of these is inferred from elapsed time.
//   - evidence_kind, record_id, record_sha256, receipt_at_ms: the exact
//     receipt that establishes the shared fate of the prompt's Messages.
//   - fenced_at_ms: when a call end or owner change fenced the prompt's job,
//     after which nothing of it passes revalidation.
//   - revision: owned by the hand-over's job and its fencing callers.
const busySendPromptsDDL = `CREATE TABLE IF NOT EXISTS busy_send_prompts (
    handover_id      TEXT NOT NULL,
    prompt_index     INTEGER NOT NULL CHECK(typeof(prompt_index) = 'integer' AND prompt_index >= 0),
    store_id         TEXT NOT NULL,
    message_count    INTEGER NOT NULL CHECK(typeof(message_count) = 'integer' AND message_count > 0),
    rendered_bytes   INTEGER NOT NULL CHECK(typeof(rendered_bytes) = 'integer' AND rendered_bytes > 0),
    route_request_id TEXT,
    client_id        TEXT,
    emission         TEXT NOT NULL DEFAULT 'none' CHECK(emission IN ('none', 'intent', 'issued', 'released')),
    intent_at_ms     INTEGER,
    issued_at_ms     INTEGER,
    released_at_ms   INTEGER,
    transport        TEXT CHECK(transport IN ('written', 'partial', 'uncertain', 'unwritten')),
    transport_at_ms  INTEGER,
    order_kind       TEXT CHECK(order_kind IN ('ordered_acceptance', 'exact_receipt', 'emission_complete')),
    order_at_ms      INTEGER,
    admission_kind   TEXT CHECK(admission_kind IN ('prompt_sighting', 'rpc_success', 'prompt_response', 'native_signal', 'transcript_match', 'exact_receipt')),
    admitted_at_ms   INTEGER,
    evidence_kind    TEXT CHECK(evidence_kind IN ('codex_item_started', 'codex_thread_items', 'cursor_end_turn')),
    record_id        TEXT,
    record_sha256    TEXT,
    receipt_at_ms    INTEGER,
    negative_kind    TEXT CHECK(negative_kind IN ('not_submitted', 'no_admission')),
    negative_at_ms   INTEGER,
    fenced_at_ms     INTEGER,
    revision         INTEGER NOT NULL DEFAULT 1 CHECK(typeof(revision) = 'integer' AND revision > 0),
    PRIMARY KEY(handover_id, prompt_index),
    CHECK(emission <> 'none' OR (intent_at_ms IS NULL AND issued_at_ms IS NULL AND released_at_ms IS NULL)),
    CHECK(emission <> 'intent' OR (intent_at_ms IS NOT NULL AND issued_at_ms IS NULL AND released_at_ms IS NULL)),
    CHECK(emission <> 'issued' OR (intent_at_ms IS NOT NULL AND issued_at_ms IS NOT NULL AND released_at_ms IS NULL)),
    CHECK(emission <> 'released' OR (issued_at_ms IS NULL AND released_at_ms IS NOT NULL)),
    CHECK((transport IS NULL) = (transport_at_ms IS NULL)),
    CHECK((order_kind IS NULL) = (order_at_ms IS NULL)),
    CHECK((admission_kind IS NULL) = (admitted_at_ms IS NULL)),
    CHECK((evidence_kind IS NULL) = (receipt_at_ms IS NULL)),
    CHECK(record_id IS NULL OR evidence_kind IS NOT NULL),
    CHECK(record_sha256 IS NULL OR evidence_kind IS NOT NULL),
    CHECK((negative_kind IS NULL) = (negative_at_ms IS NULL)),
    FOREIGN KEY(handover_id) REFERENCES busy_send_handovers(handover_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_handover_members is each hand-over's immutable ordered
// membership (member_index, in accepted order) and each member's prompt
// partition. A Message's rows here, with their hand-overs' Launches, are its
// earlier attempts; rows are never updated or deleted, so a return to Held
// clears only the Message's current membership (SR-2.1).
const busySendHandoverMembersDDL = `CREATE TABLE IF NOT EXISTS busy_send_handover_members (
    handover_id  TEXT NOT NULL,
    member_index INTEGER NOT NULL CHECK(typeof(member_index) = 'integer' AND member_index >= 0),
    message_id   TEXT NOT NULL,
    prompt_index INTEGER NOT NULL,
    store_id     TEXT NOT NULL,
    PRIMARY KEY(handover_id, member_index),
    UNIQUE(handover_id, message_id),
    FOREIGN KEY(handover_id, prompt_index) REFERENCES busy_send_prompts(handover_id, prompt_index) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(message_id) REFERENCES busy_send_messages(message_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_forces holds every force attempt, refused ones included
// (SR-2.1 "Force operation", SR-4.1, SR-4.3; M1's force), independent of any
// worker and of the agent row:
//   - force_id: the AD-made UUID returned in force_result.
//   - instance_id, conversation_id, launch_id, launch_token, provider: the
//     captured target; the last four are NULL when the attempt was refused
//     before it could capture them.
//   - owner_call_id, revision: the owning force call and the force's
//     revision; a late call end and a later eligible event arbitrate by it.
//   - handover_id: the hand-over this attempt began, NULL for an attempt
//     refused before any hand-over. Its prompts hold the force's cursor and
//     admission evidence.
//   - call_deadline_ms, call_ended_at_ms: the call's recorded finite deadline
//     and its recorded end; only a passed deadline with no recorded end
//     shows the call never reached its end.
//   - outcome, reason, started_at_ms, ended_at_ms: BusySendForceOutcome and
//     BusySendForceReason. At most one running or pending force per
//     conversation (the partial unique index).
//   - retention, expired_at_ms: an ended force is kept as long as a Final
//     confirmed Result, counted from ended_at_ms (SR-4.1).
const busySendForcesDDL = `CREATE TABLE IF NOT EXISTS busy_send_forces (
    force_id         TEXT PRIMARY KEY NOT NULL,
    store_id         TEXT NOT NULL,
    instance_id      TEXT NOT NULL,
    conversation_id  TEXT,
    launch_id        TEXT,
    launch_token     TEXT,
    provider         TEXT,
    owner_call_id    TEXT NOT NULL CHECK(owner_call_id <> ''),
    revision         INTEGER NOT NULL DEFAULT 1 CHECK(typeof(revision) = 'integer' AND revision > 0),
    handover_id      TEXT UNIQUE,
    call_deadline_ms INTEGER NOT NULL,
    call_ended_at_ms INTEGER,
    outcome          TEXT NOT NULL CHECK(outcome IN ('running', 'pending', 'handed_over', 'refused', 'failed')),
    reason           TEXT,
    started_at_ms    INTEGER NOT NULL,
    ended_at_ms      INTEGER,
    retention        TEXT NOT NULL DEFAULT 'retained' CHECK(retention IN ('retained', 'expired')),
    expired_at_ms    INTEGER,
    CHECK(call_deadline_ms >= started_at_ms),
    CHECK((outcome IN ('running', 'pending')) = (ended_at_ms IS NULL)),
    CHECK(outcome <> 'running' OR call_ended_at_ms IS NULL),
    CHECK(outcome NOT IN ('pending', 'handed_over') OR handover_id IS NOT NULL),
    CHECK((retention = 'expired') = (expired_at_ms IS NOT NULL)),
    CHECK(retention = 'retained' OR ended_at_ms IS NOT NULL),
    FOREIGN KEY(conversation_id) REFERENCES busy_conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(launch_id) REFERENCES busy_send_launches(launch_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(handover_id) REFERENCES busy_send_handovers(handover_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_force_members is a force's immutable ordered carried membership:
// the Messages it froze, in accepted order. New arrivals are never added.
const busySendForceMembersDDL = `CREATE TABLE IF NOT EXISTS busy_send_force_members (
    force_id     TEXT NOT NULL,
    member_index INTEGER NOT NULL CHECK(typeof(member_index) = 'integer' AND member_index >= 0),
    message_id   TEXT NOT NULL,
    store_id     TEXT NOT NULL,
    PRIMARY KEY(force_id, member_index),
    UNIQUE(force_id, message_id),
    FOREIGN KEY(force_id) REFERENCES busy_send_forces(force_id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY(message_id) REFERENCES busy_send_messages(message_id) DEFERRABLE INITIALLY DEFERRED
);`

// busy_send_pause_claims holds SR-6.5's pause claims, each bound to the one
// Launch it claimed, so neither an old Launch's claim nor a late callback can
// fence or answer a newer Launch. The owning call's outcome and the claim's
// fence are separate facts: the call ends when it returns, but after a signal
// the fence stands until exit evidence or a new Launch, whatever the call
// returned.
//   - owner_call_id, revision: the owning pause call and the claim's revision,
//     compared by every update, so a late callback changes nothing.
//   - claimed_at_ms, deadline_ms: the claim and its call's finite deadline
//     (the existing pause budget).
//   - signal_kind, signal_intent_at_ms: the keyless shutdown it intends and
//     the signal intent, persisted before the signal is sent.
//   - call_outcome, call_cause, call_outcome_at_ms: what the owning call
//     returned (BusySendPauseCallOutcome), its structured cause and when it
//     was recorded, once known. A later pause on the standing claim returns
//     this recorded outcome. With a signal intent and no call outcome, the
//     call never recorded its end (pause_uncertain); with neither, it may
//     still be running or may have been abandoned before signalling.
//   - state, closed_by, closed_at_ms: the fence's lifetime
//     (BusySendPauseClaimState, BusySendPauseClosure). A standing claim
//     fences its Launch, with or without a call outcome. Only a pre-signal
//     refusal or abandonment releases it, and only Launch End on exit evidence
//     or a new Launch's publication ends it; state ended is SR-6.5's "outcome
//     ended". At most one standing claim per Launch (the partial unique index).
const busySendPauseClaimsDDL = `CREATE TABLE IF NOT EXISTS busy_send_pause_claims (
    claim_id            TEXT PRIMARY KEY NOT NULL,
    store_id            TEXT NOT NULL,
    instance_id         TEXT NOT NULL,
    launch_id           TEXT NOT NULL,
    owner_call_id       TEXT NOT NULL CHECK(owner_call_id <> ''),
    revision            INTEGER NOT NULL DEFAULT 1 CHECK(typeof(revision) = 'integer' AND revision > 0),
    claimed_at_ms       INTEGER NOT NULL,
    deadline_ms         INTEGER NOT NULL,
    signal_kind         TEXT NOT NULL CHECK(signal_kind IN ('sigterm', 'server_shutdown', 'gateway_exit')),
    signal_intent_at_ms INTEGER,
    call_outcome        TEXT CHECK(call_outcome IN ('paused', 'refused', 'exit_uncertain', 'failed')),
    call_cause          TEXT,
    call_outcome_at_ms  INTEGER,
    state               TEXT NOT NULL CHECK(state IN ('standing', 'released', 'ended')),
    closed_by           TEXT CHECK(closed_by IN ('refused_before_signal', 'abandoned_before_signal', 'launch_end', 'launch_published')),
    closed_at_ms        INTEGER,
    CHECK(deadline_ms >= claimed_at_ms),
    CHECK((call_outcome IS NULL) = (call_outcome_at_ms IS NULL)),
    CHECK(call_cause IS NULL OR call_outcome IS NOT NULL),
    CHECK(call_outcome <> 'refused' OR (signal_intent_at_ms IS NULL AND state = 'released')),
    CHECK(call_outcome NOT IN ('paused', 'exit_uncertain', 'failed') OR signal_intent_at_ms IS NOT NULL),
    CHECK((state = 'standing') = (closed_by IS NULL)),
    CHECK((closed_by IS NULL) = (closed_at_ms IS NULL)),
    CHECK(state <> 'released' OR (closed_by IS NOT NULL AND closed_by IN ('refused_before_signal', 'abandoned_before_signal') AND signal_intent_at_ms IS NULL)),
    CHECK(state <> 'ended' OR (closed_by IS NOT NULL AND closed_by IN ('launch_end', 'launch_published'))),
    CHECK(closed_by <> 'refused_before_signal' OR call_outcome IS 'refused'),
    CHECK(closed_by <> 'abandoned_before_signal' OR call_outcome IS NULL),
    FOREIGN KEY(launch_id) REFERENCES busy_send_launches(launch_id) DEFERRABLE INITIALLY DEFERRED
);`

// busySendCoreObject is one permanent core table or index: its exact name,
// its sqlite_master type and its declaration.
type busySendCoreObject struct {
	name, kind, ddl string
}

// busySendCoreObjects is the permanent core, tables first and then their
// indexes, in creation order. The partial unique indexes enforce one
// published and one candidate conversation, one current and one pending
// Launch, one open hand-over and one open force per conversation, and one
// standing pause claim per Launch; the others serve the inbox's ordered
// scans and the per-Message, per-instance and per-conversation reads.
var busySendCoreObjects = []busySendCoreObject{
	{"busy_conversations", "table", busyConversationsDDL},
	{"busy_send_launches", "table", busySendLaunchesDDL},
	{"busy_send_launch_processes", "table", busySendLaunchProcessesDDL},
	{"busy_send_messages", "table", busySendMessagesDDL},
	{"busy_send_results", "table", busySendResultsDDL},
	{"busy_send_handovers", "table", busySendHandoversDDL},
	{"busy_send_prompts", "table", busySendPromptsDDL},
	{"busy_send_handover_members", "table", busySendHandoverMembersDDL},
	{"busy_send_forces", "table", busySendForcesDDL},
	{"busy_send_force_members", "table", busySendForceMembersDDL},
	{"busy_send_pause_claims", "table", busySendPauseClaimsDDL},
	{"idx_busy_conversations_published", "index", `CREATE UNIQUE INDEX IF NOT EXISTS idx_busy_conversations_published ON busy_conversations(instance_id) WHERE publication = 'published';`},
	{"idx_busy_conversations_candidate", "index", `CREATE UNIQUE INDEX IF NOT EXISTS idx_busy_conversations_candidate ON busy_conversations(instance_id) WHERE publication = 'candidate';`},
	{"idx_busy_send_launches_current", "index", `CREATE UNIQUE INDEX IF NOT EXISTS idx_busy_send_launches_current ON busy_send_launches(instance_id) WHERE phase = 'current';`},
	{"idx_busy_send_launches_pending", "index", `CREATE UNIQUE INDEX IF NOT EXISTS idx_busy_send_launches_pending ON busy_send_launches(instance_id) WHERE phase IN ('candidate', 'uncertain');`},
	{"idx_busy_send_launches_conversation", "index", `CREATE INDEX IF NOT EXISTS idx_busy_send_launches_conversation ON busy_send_launches(conversation_id);`},
	{"idx_busy_send_messages_place", "index", `CREATE INDEX IF NOT EXISTS idx_busy_send_messages_place ON busy_send_messages(conversation_id, place, accepted_seq);`},
	{"idx_busy_send_messages_handover", "index", `CREATE INDEX IF NOT EXISTS idx_busy_send_messages_handover ON busy_send_messages(handover_id);`},
	{"idx_busy_send_handovers_open", "index", `CREATE UNIQUE INDEX IF NOT EXISTS idx_busy_send_handovers_open ON busy_send_handovers(conversation_id) WHERE state = 'open';`},
	{"idx_busy_send_handover_members_message", "index", `CREATE INDEX IF NOT EXISTS idx_busy_send_handover_members_message ON busy_send_handover_members(message_id);`},
	{"idx_busy_send_forces_open", "index", `CREATE UNIQUE INDEX IF NOT EXISTS idx_busy_send_forces_open ON busy_send_forces(conversation_id) WHERE outcome IN ('running', 'pending');`},
	{"idx_busy_send_forces_instance", "index", `CREATE INDEX IF NOT EXISTS idx_busy_send_forces_instance ON busy_send_forces(instance_id);`},
	{"idx_busy_send_force_members_message", "index", `CREATE INDEX IF NOT EXISTS idx_busy_send_force_members_message ON busy_send_force_members(message_id);`},
	{"idx_busy_send_pause_claims_standing", "index", `CREATE UNIQUE INDEX IF NOT EXISTS idx_busy_send_pause_claims_standing ON busy_send_pause_claims(launch_id) WHERE state = 'standing';`},
	{"idx_busy_send_pause_claims_instance", "index", `CREATE INDEX IF NOT EXISTS idx_busy_send_pause_claims_instance ON busy_send_pause_claims(instance_id);`},
}

// busySendCoreDDL is the whole core's DDL, shared by fresh creation
// (schemaDDL) and the v20→v21 hop (the two-places rule).
var busySendCoreDDL = func() string {
	var b strings.Builder
	for _, o := range busySendCoreObjects {
		b.WriteString(o.ddl)
		b.WriteString("\n")
	}
	return b.String()
}()

// busySendSpawnColumn is one of the core's spawns columns, as
// PRAGMA table_info reports it, with its ADD COLUMN text.
type busySendSpawnColumn struct {
	name, kind string
	notNull    bool
	def        sql.NullString
	ddl        string
}

// busySendSpawnColumns are the core's spawns columns, appended after
// observation_writer in this order on a migrated store and in schemaDDL:
//   - busy_send: the current Launch's coverage (BusySendCoverage), the same
//     fact as its busy_send_launches row, read by every runtime coverage
//     decision (SR-9.1, SR-9.3).
//   - busy_conversation_id: the published conversation the current Launch is
//     bound to. No foreign key: the binding is written with the Launch's own
//     records, and an agent row's deletion must not reach the core.
//   - turn_starts: the current Launch's Turn Start counter (b.7si t1.7si.nc
//     SR-1.4), never negative: every write that begins a Launch resets it to
//     0, and a hand-over captures it with its emission intent.
var busySendSpawnColumns = []busySendSpawnColumn{
	{"busy_send", "TEXT", true, sql.NullString{String: "'not_covered'", Valid: true},
		`ALTER TABLE spawns ADD COLUMN busy_send TEXT NOT NULL DEFAULT 'not_covered' CHECK(busy_send IN ('not_covered', 'covered'))`},
	{"busy_conversation_id", "TEXT", false, sql.NullString{},
		`ALTER TABLE spawns ADD COLUMN busy_conversation_id TEXT`},
	{"turn_starts", "INTEGER", true, sql.NullString{String: "0", Valid: true},
		`ALTER TABLE spawns ADD COLUMN turn_starts INTEGER NOT NULL DEFAULT 0 CHECK(typeof(turn_starts) = 'integer' AND turn_starts >= 0)`},
}

// The core's identity in store_meta: busySendCoreKey's value is
// busySendCoreVersion followed by the store id. It commits with fresh
// creation or the v20→v21 hop, last before the stamp, and every open at
// schemaVersion checks it with the core's declarations.
const (
	busySendCoreKey     = "busy_send_core"
	busySendCoreVersion = "busy-send-core-v1:"
)

// errBusySendCoreUnrecognized wraps every refusal of an open whose core is
// not the permanent contract. Open never repairs, rebuilds or converts it.
var errBusySendCoreUnrecognized = fmt.Errorf("%w: busy-send core not recognized", ErrSchemaMismatch)

// busySendCoreIncompatible is a proven incompatibility: the named core object
// or spawns column exists with a declaration other than its permanent one.
// Only this, never a failure to read the schema, makes recognition or the hop
// report ErrSchemaMismatch; a busy or failed read keeps its own error, so a
// caller never takes an operational failure for an incompatible store.
type busySendCoreIncompatible string

func (e busySendCoreIncompatible) Error() string { return "incompatible " + string(e) }

// isBusySendCoreIncompatible reports whether err is a proven incompatibility.
func isBusySendCoreIncompatible(err error) bool {
	var incompatible busySendCoreIncompatible
	return errors.As(err, &incompatible)
}

// normalizedDDL compares declarations whitespace aside. SQLite records each
// CREATE without its IF NOT EXISTS, so the comparison drops it from both.
func normalizedDDL(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), ";")
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "IF NOT EXISTS ", "")), " ")
}

// coreSQL is satisfied by a *sql.Tx and a *sql.DB, so the hop's validation
// and open's recognition share their checks.
type coreSQL interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// schemaObject is one sqlite_master entry: its exact name, its type and its
// recorded declaration.
type schemaObject struct {
	name, kind string
	ddl        sql.NullString
}

// readSchemaCatalogue reads sqlite_master once, keyed by lower-case name:
// SQLite names are unique regardless of case, so a key finds an object
// whatever case it was created with, and the checks compare the exact name.
func readSchemaCatalogue(ctx context.Context, q coreSQL) (map[string]schemaObject, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, type, sql FROM sqlite_master`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	catalogue := make(map[string]schemaObject)
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.name, &o.kind, &o.ddl); err != nil {
			return nil, err
		}
		catalogue[strings.ToLower(o.name)] = o
	}
	return catalogue, rows.Err()
}

// busySendCoreObjectsPresent compares every core object the catalogue holds
// with its permanent declaration and returns which exist. A name that
// matches only case-insensitively, a different type or a different
// declaration is a busySendCoreIncompatible naming the object.
func busySendCoreObjectsPresent(catalogue map[string]schemaObject) (map[string]bool, error) {
	present := make(map[string]bool, len(busySendCoreObjects))
	for _, o := range busySendCoreObjects {
		got, ok := catalogue[o.name]
		if !ok {
			continue
		}
		if got.name != o.name || got.kind != o.kind || !got.ddl.Valid || normalizedDDL(got.ddl.String) != normalizedDDL(o.ddl) {
			return nil, busySendCoreIncompatible(o.name)
		}
		present[o.name] = true
	}
	return present, nil
}

// busySendSpawnColumnsPresent compares the core's spawns columns the store
// already has with their permanent shape and declaration and returns which
// exist. PRAGMA table_info gives name, type, nullability, default and key;
// the CHECK constraints are only in spawns' recorded declaration, so each
// present column must also be declared there exactly as busySendSpawnColumns
// declares it (busySendSpawnColumnDeclared).
func busySendSpawnColumnsPresent(ctx context.Context, q coreSQL, catalogue map[string]schemaObject) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `PRAGMA table_info(spawns)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	present := make(map[string]bool, len(busySendSpawnColumns))
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &def, &pk); err != nil {
			return nil, err
		}
		for _, c := range busySendSpawnColumns {
			if !strings.EqualFold(name, c.name) {
				continue
			}
			if name != c.name || kind != c.kind || (notNull == 1) != c.notNull || def != c.def || pk != 0 {
				return nil, busySendCoreIncompatible("spawns." + c.name)
			}
			present[c.name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	spawns := catalogue["spawns"]
	for _, c := range busySendSpawnColumns {
		if present[c.name] && !busySendSpawnColumnDeclared(spawns.ddl.String, c) {
			return nil, busySendCoreIncompatible("spawns." + c.name)
		}
	}
	return present, nil
}

// busySendSpawnColumnDeclared reports whether spawnsDDL, spawns' recorded
// CREATE TABLE, declares c exactly as its ADD COLUMN text does, whitespace
// aside, as one whole column definition, and names c nowhere else. SQLite
// records an added column by appending its definition text verbatim to the
// table's declaration, so a fresh store and a migrated one both pass, while
// a missing, altered or extra CHECK, a table constraint on the column, or a
// foreign key on it fails.
func busySendSpawnColumnDeclared(spawnsDDL string, c busySendSpawnColumn) bool {
	table := strings.Join(strings.Fields(spawnsDDL), " ")
	column := strings.Join(strings.Fields(strings.TrimPrefix(c.ddl, "ALTER TABLE spawns ADD COLUMN ")), " ")
	i := strings.Index(table, column)
	if i < 0 {
		return false
	}
	before := strings.TrimRight(table[:i], " ")
	after := strings.TrimLeft(table[i+len(column):], " ")
	if !strings.HasSuffix(before, ",") && !strings.HasSuffix(before, "(") {
		return false
	}
	if !strings.HasPrefix(after, ",") && !strings.HasPrefix(after, ")") {
		return false
	}
	return identifierCount(table, c.name) == identifierCount(column, c.name)
}

// identifierCount counts the SQL identifier tokens of s equal to name,
// ignoring case and quoting.
func identifierCount(s, name string) int {
	n := 0
	for _, token := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if token == name {
			n++
		}
	}
	return n
}

// recognizeBusySendCore is open's recognition of the permanent core at
// schemaVersion (SR-7.2): the store_meta identity names this store, and every
// core table, index and spawns column has its permanent declaration. It only
// reads. A proven difference refuses with errBusySendCoreUnrecognized; a
// failure to read the schema returns its own error, without ErrSchemaMismatch.
// Open never migrates, repairs, rebuilds or dispatches. This check is the
// same for every later version, whatever its release.
func recognizeBusySendCore(ctx context.Context, q coreSQL, storeID string) error {
	var identity string
	err := q.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, busySendCoreKey).Scan(&identity)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && identity != busySendCoreVersion+storeID) {
		return fmt.Errorf("%w: core identity", errBusySendCoreUnrecognized)
	}
	if err != nil {
		return fmt.Errorf("store: read busy-send core identity: %w", err)
	}
	catalogue, err := readSchemaCatalogue(ctx, q)
	if err != nil {
		return fmt.Errorf("store: read busy-send core declarations: %w", err)
	}
	objects, err := busySendCoreObjectsPresent(catalogue)
	if err != nil {
		return fmt.Errorf("%w: %w", errBusySendCoreUnrecognized, err)
	}
	columns, err := busySendSpawnColumnsPresent(ctx, q, catalogue)
	if isBusySendCoreIncompatible(err) {
		return fmt.Errorf("%w: %w", errBusySendCoreUnrecognized, err)
	}
	if err != nil {
		return fmt.Errorf("store: read busy-send core declarations: %w", err)
	}
	for _, o := range busySendCoreObjects {
		if !objects[o.name] {
			return fmt.Errorf("%w: missing %s", errBusySendCoreUnrecognized, o.name)
		}
	}
	for _, c := range busySendSpawnColumns {
		if !columns[c.name] {
			return fmt.Errorf("%w: missing spawns.%s", errBusySendCoreUnrecognized, c.name)
		}
	}
	return nil
}

// busySendCoreTables are the core tables, the ones whose existing rows the
// hop checks for this store's id and for references to missing records.
func busySendCoreTables() []string {
	var tables []string
	for _, o := range busySendCoreObjects {
		if o.kind == "table" {
			tables = append(tables, o.name)
		}
	}
	return tables
}

// migrateV20toV21 is the exempt install's one schema hop (SR-7.1, SR-7.2): it
// creates the permanent core, records every existing instance's current
// conversation and Launch with their coverage, and commits the core identity
// and the v21 stamp, last, in one transaction on a synchronous=FULL
// connection. It delivers nothing and records no route, wake, receipt or
// emission: an existing Launch's route is unknown until its next resume or
// reuse. The administrator sentinel gate applies as for every earlier hop.
//
// Phase 1 validates before any change: the store id; any existing core
// identity, core object and spawns column against its permanent declaration;
// the values in existing spawns columns; and every row's agent and life.
// Existing core rows must name this store and refer only to records that
// exist, and core facts already recorded must be consistent: a bound row's
// conversation is its instance's one published conversation and its Launch
// the instance's one current Launch, each with the row's provider, and the
// Launch with the row's life, launch token and coverage; no row still to be
// recorded has a published or candidate conversation or a current or pending
// Launch. A proven difference refuses with ErrSchemaMismatch; a failure to
// read keeps its own error. Either way the v20 schema, data, stamp and
// sentinel stay as they were.
//
// Phase 2 creates the missing core objects and spawns columns. Phase 3
// records each row with no conversation binding: a published conversation
// (the instance's next generation) and a current migrated Launch, both with
// the row's provider (busySendProviderSQL: a legacy NULL or empty agent is
// claude, and spawns.agent is not rewritten) and the coverage
// BusySendCoverageForAgent gives that provider, or covered if the row's
// busy_send already said so: coverage never decreases. A row that already
// has a binding keeps all its recorded facts, so a compatible reentry never
// reclassifies an established Launch or conversation. No existing value,
// row_version included, changes. Phase 4 inserts the core identity and
// stamps 21.
func migrateV20toV21(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := inputDurability(ctx, conn); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	storeID, plan, err := validateV20toV21(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, busySendCoreDDL); err != nil {
		return fmt.Errorf("store: v20→v21 create busy-send core: %w", err)
	}
	for _, c := range busySendSpawnColumns {
		if !plan.columns[c.name] {
			if _, err := tx.ExecContext(ctx, c.ddl); err != nil {
				return fmt.Errorf("store: v20→v21 add spawns.%s: %w", c.name, err)
			}
		}
	}
	if err := backfillV21Conversations(ctx, tx, storeID, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("store: v20→v21 record conversations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO store_meta(key, value) SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM store_meta WHERE key = ?)`,
		busySendCoreKey, busySendCoreVersion+storeID, busySendCoreKey); err != nil {
		return fmt.Errorf("store: v20→v21 record busy-send core identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 21`); err != nil {
		return fmt.Errorf("store: v20→v21 stamp user_version: %w", err)
	}
	return commitMigrationStep(tx)
}

// v21Plan is what phase 1 found already present: core objects and spawns
// columns a partial earlier state created.
type v21Plan struct {
	objects, columns map[string]bool
}

// validateV20toV21 is migrateV20toV21's phase 1. It only reads.
func validateV20toV21(ctx context.Context, tx *sql.Tx) (string, v21Plan, error) {
	var plan v21Plan
	var storeID string
	err := tx.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = 'store_id'`).Scan(&storeID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", plan, fmt.Errorf("%w: store: v20→v21 no store id", ErrSchemaMismatch)
	case err != nil:
		return "", plan, fmt.Errorf("store: v20→v21 read store id: %w", err)
	case !validStoreID(storeID):
		return "", plan, fmt.Errorf("%w: store: v20→v21 invalid store id", ErrSchemaMismatch)
	}
	var identity string
	err = tx.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, busySendCoreKey).Scan(&identity)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", plan, fmt.Errorf("store: v20→v21 read busy-send core identity: %w", err)
	case identity != busySendCoreVersion+storeID:
		return "", plan, fmt.Errorf("%w: store: v20→v21 incompatible busy-send core identity", ErrSchemaMismatch)
	}
	catalogue, err := readSchemaCatalogue(ctx, tx)
	if err != nil {
		return "", plan, fmt.Errorf("store: v20→v21 read declarations: %w", err)
	}
	if plan.objects, err = busySendCoreObjectsPresent(catalogue); err != nil {
		return "", plan, fmt.Errorf("%w: store: v20→v21 %w", ErrSchemaMismatch, err)
	}
	plan.columns, err = busySendSpawnColumnsPresent(ctx, tx, catalogue)
	if isBusySendCoreIncompatible(err) {
		return "", plan, fmt.Errorf("%w: store: v20→v21 %w", ErrSchemaMismatch, err)
	}
	if err != nil {
		return "", plan, fmt.Errorf("store: v20→v21 read spawns columns: %w", err)
	}
	for _, check := range v21ValueChecks(plan, storeID) {
		var invalid bool
		if err := tx.QueryRowContext(ctx, check.query, check.args...).Scan(&invalid); err != nil {
			return "", plan, fmt.Errorf("store: v20→v21 check %s: %w", check.refusal, err)
		}
		if invalid {
			return "", plan, fmt.Errorf("%w: store: v20→v21 %s", ErrSchemaMismatch, check.refusal)
		}
	}
	return storeID, plan, nil
}

// v21Check is one of phase 1's read-only refusal checks: query selects true
// when the store must be refused with refusal.
type v21Check struct {
	query   string
	args    []any
	refusal string
}

// busySendProviderSQL is the provider the core records for a spawns row's
// agent column: the agent itself, or claude for a legacy row whose agent is
// NULL or empty, the rule every reader of such rows already applies
// (COALESCE(agent,”) IN (”,'claude'), session_history.go). spawns.agent
// itself is never rewritten. column is a fixed literal, never input.
func busySendProviderSQL(column string) string {
	return `CASE WHEN COALESCE(` + column + `, '') = '' THEN '` + AgentClaude + `' ELSE ` + column + ` END`
}

// busySendLaunchTokenSQL is the launch token the core records for a spawns
// row: its stored text, or NULL for NULL or a non-text value, which no reader
// takes as a token. column is a fixed literal, never input.
func busySendLaunchTokenSQL(column string) string {
	return `CASE WHEN typeof(` + column + `) = 'text' THEN ` + column + ` END`
}

// v21ValueChecks lists phase 1's checks of existing values, given which
// columns and tables a partial state already has. Rows still to be recorded
// are those with no conversation binding (all of them before the binding
// column exists). Every row's agent must be text or NULL (a NULL or empty one
// is a legacy claude row) and its life a nonnegative integer, because the
// core records both.
func v21ValueChecks(plan v21Plan, storeID string) []v21Check {
	checks := []v21Check{{
		query:   `SELECT EXISTS (SELECT 1 FROM spawns WHERE typeof(agent) NOT IN ('text', 'null') OR typeof(life_number) <> 'integer' OR life_number < 0)`,
		refusal: "invalid spawns.agent or spawns.life_number",
	}}
	if plan.columns["busy_send"] {
		checks = append(checks, v21Check{
			query:   `SELECT EXISTS (SELECT 1 FROM spawns WHERE typeof(busy_send) <> 'text' OR busy_send NOT IN (?, ?))`,
			args:    []any{string(BusySendNotCovered), string(BusySendCovered)},
			refusal: "invalid spawns.busy_send",
		})
	}
	if plan.columns["turn_starts"] {
		checks = append(checks, v21Check{
			query:   `SELECT EXISTS (SELECT 1 FROM spawns WHERE typeof(turn_starts) <> 'integer' OR turn_starts < 0)`,
			refusal: "invalid spawns.turn_starts",
		})
	}
	tables := plan.objects["busy_conversations"] && plan.objects["busy_send_launches"]
	if plan.columns["busy_conversation_id"] {
		checks = append(checks, v21Check{
			query:   `SELECT EXISTS (SELECT 1 FROM spawns WHERE busy_conversation_id IS NOT NULL AND typeof(busy_conversation_id) <> 'text')`,
			refusal: "invalid spawns.busy_conversation_id",
		})
		if !tables || !plan.columns["busy_send"] {
			// A binding with nothing recorded to bind to is ambiguous.
			checks = append(checks, v21Check{
				query:   `SELECT EXISTS (SELECT 1 FROM spawns WHERE busy_conversation_id IS NOT NULL)`,
				refusal: "ambiguous spawns.busy_conversation_id",
			})
		} else {
			// The bound conversation and Launch must be the row's own: its
			// provider, and for the Launch its life, launch token (NULL
			// matching NULL) and coverage.
			checks = append(checks, v21Check{
				query: `SELECT EXISTS (SELECT 1 FROM spawns s WHERE s.busy_conversation_id IS NOT NULL AND (
				    NOT EXISTS (SELECT 1 FROM busy_conversations c WHERE c.conversation_id = s.busy_conversation_id
				        AND c.instance_id = s.instance_id AND c.publication = 'published'
				        AND c.provider = ` + busySendProviderSQL("s.agent") + `
				        AND (c.busy_send = 'covered' OR s.busy_send = 'not_covered'))
				    OR NOT EXISTS (SELECT 1 FROM busy_send_launches l WHERE l.instance_id = s.instance_id
				        AND l.phase = 'current' AND l.conversation_id = s.busy_conversation_id AND l.busy_send = s.busy_send
				        AND l.provider = ` + busySendProviderSQL("s.agent") + ` AND l.life_number = s.life_number
				        AND l.launch_token IS ` + busySendLaunchTokenSQL("s.launch_token") + `)))`,
				refusal: "ambiguous spawns.busy_conversation_id",
			})
		}
	}
	if plan.objects["busy_conversations"] {
		checks = append(checks, v21Check{
			query: `SELECT EXISTS (SELECT 1 FROM busy_conversations WHERE publication = 'published'
			    GROUP BY instance_id HAVING count(*) > 1)`,
			refusal: "ambiguous busy_conversations",
		})
	}
	if plan.objects["busy_send_launches"] {
		checks = append(checks, v21Check{
			query: `SELECT EXISTS (SELECT 1 FROM busy_send_launches WHERE phase = 'current'
			    GROUP BY instance_id HAVING count(*) > 1)`,
			refusal: "ambiguous busy_send_launches",
		})
	}
	for _, table := range busySendCoreTables() {
		if !plan.objects[table] {
			continue
		}
		// Table names come from the fixed core list, never from input.
		checks = append(checks, v21Check{
			query:   `SELECT EXISTS (SELECT 1 FROM ` + table + ` WHERE store_id IS NOT ?)`,
			args:    []any{storeID},
			refusal: table + " names another store",
		}, v21Check{
			query:   `SELECT EXISTS (SELECT 1 FROM pragma_foreign_key_check(?))`,
			args:    []any{table},
			refusal: table + " refers to a missing record",
		})
	}
	if plan.objects["busy_conversations"] {
		bound := `0`
		if plan.columns["busy_conversation_id"] {
			bound = `EXISTS (SELECT 1 FROM spawns s WHERE s.instance_id = c.instance_id AND s.busy_conversation_id = c.conversation_id)`
		}
		checks = append(checks, v21Check{
			query: `SELECT EXISTS (SELECT 1 FROM busy_conversations c WHERE c.publication = 'candidate'
			    OR (c.publication = 'published' AND NOT ` + bound + `))`,
			refusal: "ambiguous busy_conversations",
		})
	}
	if plan.objects["busy_send_launches"] {
		bound := `0`
		if plan.columns["busy_conversation_id"] {
			bound = `EXISTS (SELECT 1 FROM spawns s WHERE s.instance_id = l.instance_id AND s.busy_conversation_id = l.conversation_id)`
		}
		checks = append(checks, v21Check{
			query: `SELECT EXISTS (SELECT 1 FROM busy_send_launches l WHERE l.phase IN ('candidate', 'uncertain')
			    OR (l.phase = 'current' AND NOT ` + bound + `))`,
			refusal: "ambiguous busy_send_launches",
		})
	}
	return checks
}

// v21UnboundRow is one spawns row phase 3 records.
type v21UnboundRow struct {
	instanceID, provider string
	life                 int64
	launchToken          sql.NullString
	covered              bool
}

// backfillV21Conversations is migrateV20toV21's phase 3: for every spawns row
// with no conversation binding, of every agent and state, one published
// conversation and one current Launch record bound to it, with the row's
// provider (busySendProviderSQL). The Launch record copies the row's life and
// its launch token (busySendLaunchTokenSQL); it records no session, route,
// wake path or process, which the core never had. Every existing row keeps
// every other value, its agent included.
func backfillV21Conversations(ctx context.Context, tx *sql.Tx, storeID string, nowMs int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT instance_id, `+busySendProviderSQL("agent")+`, life_number,
	        `+busySendLaunchTokenSQL("launch_token")+`, busy_send
	   FROM spawns WHERE busy_conversation_id IS NULL ORDER BY instance_id`)
	if err != nil {
		return err
	}
	var unbound []v21UnboundRow
	for rows.Next() {
		var r v21UnboundRow
		var coverage string
		if err := rows.Scan(&r.instanceID, &r.provider, &r.life, &r.launchToken, &coverage); err != nil {
			rows.Close()
			return err
		}
		r.covered = coverage == string(BusySendCovered) || BusySendCoverageForAgent(r.provider) == BusySendCovered
		unbound = append(unbound, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range unbound {
		conversationID, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		launchID, err := uuid.NewRandom()
		if err != nil {
			return err
		}
		coverage := BusySendNotCovered
		if r.covered {
			coverage = BusySendCovered
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO busy_conversations (
		        conversation_id, store_id, instance_id, generation, publication, provider,
		        busy_send, recorded_at_ms, published_at_ms)
		    SELECT ?, ?, ?, COALESCE(MAX(generation), 0) + 1, ?, ?, ?, ?, ?
		      FROM busy_conversations WHERE instance_id = ?`,
			conversationID.String(), storeID, r.instanceID, string(BusySendPublished), r.provider,
			string(coverage), nowMs, nowMs, r.instanceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO busy_send_launches (
		        launch_id, store_id, instance_id, conversation_id, life_number, launch_token,
		        provider, busy_send, kind, phase, recorded_at_ms)
		    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			launchID.String(), storeID, r.instanceID, conversationID.String(), r.life, r.launchToken,
			r.provider, string(coverage), string(BusySendLaunchMigrated), string(BusySendLaunchCurrent), nowMs); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE spawns SET busy_send = ?, busy_conversation_id = ? WHERE instance_id = ?`,
			string(coverage), conversationID.String(), r.instanceID); err != nil {
			return err
		}
	}
	return nil
}
