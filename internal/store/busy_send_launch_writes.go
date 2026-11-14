package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// All helpers in this file run under the lifecycle writer's existing write
// lock. Candidates describe external creation, independently of the published
// conversation/current Launch that reads and future admissions still use.
//
// busySendPublishedRow is the one typed read of an instance's published
// binding that lifecycle writers, admission and the delivery reads share
// (readBusySendPublished). Every field is the published conversation's or the
// current Launch's own, never a staged candidate's, so a read during a pending
// resume or reuse cannot pair the published conversation with the candidate's
// token or provider.
type busySendPublishedRow struct {
	// conversation and provider: the published conversation and its provider,
	// published with it by InsertPending (spawn) or a reuse's publication
	// (publishBusySendCandidate); a resume keeps both.
	conversation, launch, provider string
	// token is the current Launch's own recorded launch token ("" for NULL),
	// never spawns.launch_token, which can already name a staged candidate.
	token string
	// coverage is the published conversation's coverage, which the lifecycle
	// writers read (resume's monotonic rule, publication's guard).
	// launchCoverage is the current Launch's own recorded coverage, the same
	// fact as spawns.busy_send, which every call about the current Launch
	// decides from (SR-9.1); a covered conversation can have an uncovered
	// current Launch.
	coverage             BusySendCoverage
	launchCoverage       BusySendCoverage
	conversationRevision int64
	launchRevision       int64
	// defaults are the conversation's Agent Defaults, published with it by
	// InsertPending or a reuse's publication; staging never changes them.
	defaults BusySendAgentDefaults
	// acceptedSeq, reservedCount and reservedBytes are the inbox's admission
	// facts, written by admission and the custody writers; ownerHandover is
	// its open hand-over ("" for none), written by the hand-over writers.
	acceptedSeq, reservedCount, reservedBytes int64
	ownerHandover                             string
	// routeKind and wakePath are the current Launch's route and content-free
	// wake endpoint, "" until its route records them (SR-7.2); launchEnded is
	// whether its Launch End is recorded.
	routeKind, wakePath string
	launchEnded         bool
	// state is the spawns row's raw lifecycle state.
	state string
}

// busySendLaunchSQL is the locked transaction surface shared by lifecycle
// writers using a Conn and the SessionStart writer using a Tx.
type busySendLaunchSQL interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func readBusySendPublished(ctx context.Context, conn busySendLaunchSQL, instanceID string) (busySendPublishedRow, error) {
	var p busySendPublishedRow
	var raw, launchRaw any
	var mode string
	var token, owner, route, wake sql.NullString
	var ended sql.NullInt64
	err := conn.QueryRowContext(ctx, `SELECT c.conversation_id, c.revision, c.provider, c.busy_send,
	        c.default_send_mode, c.default_confirmed, c.confirm_from_transcripts,
	        c.accepted_seq, c.reserved_count, c.reserved_bytes, c.owner_handover_id,
	        l.launch_id, l.revision, l.launch_token, l.busy_send, l.route_kind, l.wake_path, l.ended_at_ms, s.state
	    FROM spawns s JOIN busy_conversations c ON c.conversation_id = s.busy_conversation_id
	        AND c.instance_id = s.instance_id AND c.publication = 'published'
	    JOIN busy_send_launches l ON l.instance_id = s.instance_id
	        AND l.conversation_id = c.conversation_id AND l.phase = 'current'
	        AND l.busy_send = s.busy_send AND l.provider = c.provider
	        AND (c.busy_send = 'covered' OR l.busy_send = 'not_covered')
	    WHERE s.instance_id = ?`, instanceID).Scan(&p.conversation, &p.conversationRevision,
		&p.provider, &raw, &mode, &p.defaults.Confirmed, &p.defaults.ConfirmFromTranscripts,
		&p.acceptedSeq, &p.reservedCount, &p.reservedBytes, &owner,
		&p.launch, &p.launchRevision, &token, &launchRaw, &route, &wake, &ended, &p.state)
	if err != nil {
		return p, fmt.Errorf("store: read published busy-send Launch: %w", err)
	}
	p.token, p.ownerHandover, p.routeKind, p.wakePath = token.String, owner.String, route.String, wake.String
	p.defaults.SendMode, p.launchEnded = BusySendMode(mode), ended.Valid
	if p.coverage, err = decodeBusySendCoverage(raw); err != nil {
		return p, err
	}
	p.launchCoverage, err = decodeBusySendCoverage(launchRaw)
	return p, err
}

// busySendChange requires the captured entity revision to still own a write.
// A failed comparison rolls the caller's entire lifecycle transaction back.
func busySendChange(ctx context.Context, conn busySendLaunchSQL, query string, args ...any) error {
	r, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("store: busy-send lifecycle revision changed")
	}
	return nil
}

// insertBusySendConversation records a new conversation with the Agent
// Defaults its launch resolved: published at once for a spawn, or a reuse's
// candidate, whose defaults become visible only when it is published.
func (s *Store) insertBusySendConversation(ctx context.Context, conn *sql.Conn, instanceID, provider string, coverage BusySendCoverage, defaults BusySendAgentDefaults, publication BusySendPublication, at int64) (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	var published any
	if publication == BusySendPublished {
		published = at
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO busy_conversations
	    (conversation_id, store_id, instance_id, generation, publication, provider, busy_send,
	     default_send_mode, default_confirmed, confirm_from_transcripts, recorded_at_ms, published_at_ms)
	    SELECT ?, ?, ?, COALESCE(MAX(generation), 0) + 1, ?, ?, ?, ?, ?, ?, ?, ?
	    FROM busy_conversations WHERE instance_id = ?`, id.String(), s.StoreID(), instanceID,
		string(publication), provider, string(coverage), string(defaults.SendMode), boolInt(defaults.Confirmed),
		boolInt(defaults.ConfirmFromTranscripts), at, published, instanceID)
	return id.String(), err
}

// insertBusySendLaunch records a Launch; overrides are a resume candidate's
// staged Agent Default overrides (busy_send_launches.override_*), empty for
// every other Launch.
func (s *Store) insertBusySendLaunch(ctx context.Context, conn *sql.Conn, instanceID, conversation, provider string, coverage BusySendCoverage, kind BusySendLaunchKind, phase BusySendLaunchPhase, overrides BusySendDefaultsChoice, at int64) error {
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	var mode, confirmed, fromTranscripts any
	if overrides.SendMode != nil {
		mode = string(*overrides.SendMode)
	}
	if overrides.Confirmed != nil {
		confirmed = boolInt(*overrides.Confirmed)
	}
	if overrides.ConfirmFromTranscripts != nil {
		fromTranscripts = boolInt(*overrides.ConfirmFromTranscripts)
	}
	return busySendChange(ctx, conn, `INSERT INTO busy_send_launches
	    (launch_id, store_id, instance_id, conversation_id, life_number, launch_token, provider, busy_send, kind, phase,
	     override_send_mode, override_confirmed, override_confirm_from_transcripts, recorded_at_ms)
	    SELECT ?, ?, instance_id, ?, life_number, `+busySendLaunchTokenSQL("launch_token")+`, ?, ?, ?, ?, ?, ?, ?, ?
	    FROM spawns WHERE instance_id = ?`, id.String(), s.StoreID(), conversation, provider,
		string(coverage), string(kind), string(phase), mode, confirmed, fromTranscripts, at, instanceID)
}

// insertBusySendPublished is part of InsertPending: a new instance has no
// previous conversation to retain while its first process is being created,
// so its conversation and Agent Defaults are published at once.
func (s *Store) insertBusySendPublished(ctx context.Context, conn *sql.Conn, sp Spawn) error {
	coverage := BusySendCoverageForAgent(sp.Agent)
	conversation, err := s.insertBusySendConversation(ctx, conn, sp.InstanceID, sp.Agent, coverage,
		sp.BusySendDefaults.Over(BusySendOutOfBoxDefaults), BusySendPublished, sp.LaunchStartedAtMillis)
	if err != nil {
		return err
	}
	if err := s.insertBusySendLaunch(ctx, conn, sp.InstanceID, conversation, sp.Agent, coverage,
		BusySendLaunchSpawn, BusySendLaunchCurrent, BusySendDefaultsChoice{}, sp.LaunchStartedAtMillis); err != nil {
		return err
	}
	return busySendChange(ctx, conn, `UPDATE spawns SET busy_send = ?, busy_conversation_id = ? WHERE instance_id = ?`,
		string(coverage), conversation, sp.InstanceID)
}

// stageBusySendLaunch is called only after a guarded finished->pending write
// applied. The API's existing exclusion checks and that same snapshot guard
// allow replacing an earlier uncertain creation; this helper grants no new
// permission to proceed. Replacement proves neither exit nor old-issuer
// exclusion, so its logical cause is candidate_replaced, never process_exited.
// A reuse's candidate conversation carries defaults resolved over the
// out-of-box values; a resume's candidate Launch carries defaults as its
// overrides of the published conversation's. Either way the published
// conversation keeps its own values until the candidate's publication
// (SR-6.3).
func (s *Store) stageBusySendLaunch(ctx context.Context, conn *sql.Conn, instanceID string, kind BusySendLaunchKind, defaults BusySendDefaultsChoice, at int64) error {
	p, err := readBusySendPublished(ctx, conn, instanceID)
	if err != nil {
		return err
	}
	if err := closeBusySendCandidate(ctx, conn, instanceID, nil, BusySendLaunchSuperseded, BusySendLaunchCandidateReplaced, at); err != nil {
		return err
	}
	var provider string
	if err := conn.QueryRowContext(ctx, `SELECT `+busySendProviderSQL("agent")+` FROM spawns WHERE instance_id = ?`, instanceID).Scan(&provider); err != nil {
		return err
	}
	conversation := p.conversation
	coverage := ResumeLaunchCoverage(p.coverage, provider)
	overrides := defaults
	if kind == BusySendLaunchReuse {
		coverage, overrides = BusySendCoverageForAgent(provider), BusySendDefaultsChoice{}
		conversation, err = s.insertBusySendConversation(ctx, conn, instanceID, provider, coverage,
			defaults.Over(BusySendOutOfBoxDefaults), BusySendCandidate, at)
		if err != nil {
			return err
		}
	} else if provider != p.provider {
		return fmt.Errorf("store: resume changes busy-send provider")
	}
	return s.insertBusySendLaunch(ctx, conn, instanceID, conversation, provider, coverage, kind, BusySendLaunchCandidate, overrides, at)
}

// closeBusySendCandidate preserves evidence instead of deleting a failed or
// replaced candidate. A nil token is used only by a new guarded Launch start;
// restoration always names the captured candidate token.
func closeBusySendCandidate(ctx context.Context, conn *sql.Conn, instanceID string, token *string, phase BusySendLaunchPhase, cause BusySendLaunchEnd, at int64) error {
	var launch, conversation string
	var revision int64
	var capturedToken any
	if token != nil {
		capturedToken = nullableStringArg(*token)
	}
	err := conn.QueryRowContext(ctx, `SELECT launch_id, conversation_id, revision FROM busy_send_launches
	    WHERE instance_id = ? AND phase IN ('candidate', 'uncertain') AND (? OR launch_token IS ?)`,
		instanceID, token == nil, capturedToken).Scan(&launch, &conversation, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := busySendChange(ctx, conn, `UPDATE busy_send_launches SET phase = ?, ended_at_ms = COALESCE(ended_at_ms, ?), end_cause = COALESCE(end_cause, ?), revision = revision + 1
	    WHERE launch_id = ? AND revision = ? AND phase IN ('candidate', 'uncertain')`, string(phase), at, string(cause), launch, revision); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `UPDATE busy_conversations SET publication = 'abandoned', revision = revision + 1
	    WHERE conversation_id = ? AND publication = 'candidate'`, conversation)
	return err
}

// publishBusySendCandidate captures and compares the published conversation,
// current Launch and candidate revisions under the same write lock. Admission
// changes that committed during creation are included, never overwritten by a
// pre-create conversation snapshot. Every call is inside an existing write
// establishing successful creation/report-in, never a read or a timer. The
// candidate token, not the creator's obsolete spawns version, pins the Launch;
// replacement/restoration/deletion cannot promote it. Repeated publication is
// a no-op. The return value says whether this call promoted a candidate; its
// caller owns the spawns version advance. E1 has no custody to finalize. A
// reuse's Agent Defaults publish with its candidate conversation; a resume
// candidate's staged overrides are applied to the published conversation
// here, in the same guarded update as its coverage, whichever write publishes
// it (the creator, adoption or the first hook report-in).
func publishBusySendCandidate(ctx context.Context, conn busySendLaunchSQL, instanceID, token string, at int64) (bool, error) {
	var launch, conversation, kind string
	var revision int64
	var raw, mode, confirmed, fromTranscripts any
	err := conn.QueryRowContext(ctx, `SELECT launch_id, conversation_id, kind, revision, busy_send,
	        override_send_mode, override_confirmed, override_confirm_from_transcripts
	    FROM busy_send_launches l WHERE instance_id = ? AND launch_token IS ? AND phase IN ('candidate', 'uncertain')
	        AND EXISTS (SELECT 1 FROM spawns s WHERE s.instance_id = l.instance_id AND s.launch_token IS l.launch_token)`,
		instanceID, nullableStringArg(token)).Scan(&launch, &conversation, &kind, &revision, &raw, &mode, &confirmed, &fromTranscripts)
	if errors.Is(err, sql.ErrNoRows) {
		// Already published, replaced, restored, deleted, or a plain spawn
		// that published in InsertPending. None authorizes a second commit.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	coverage, err := decodeBusySendCoverage(raw)
	if err != nil {
		return false, err
	}
	p, err := readBusySendPublished(ctx, conn, instanceID)
	if err != nil {
		return false, err
	}
	if kind == string(BusySendLaunchReuse) {
		var candidateRevision int64
		if err := conn.QueryRowContext(ctx, `SELECT revision FROM busy_conversations
	        WHERE conversation_id = ? AND instance_id = ? AND publication = 'candidate'`, conversation, instanceID).Scan(&candidateRevision); err != nil {
			return false, err
		}
		if err := busySendChange(ctx, conn, `UPDATE busy_conversations SET publication = 'retired', retired_at_ms = ?,
	        retired_reason = 'conversation_replaced', replaced_by = ?, revision = revision + 1
	        WHERE conversation_id = ? AND revision = ? AND publication = 'published'`, at, conversation, p.conversation, p.conversationRevision); err != nil {
			return false, err
		}
		if err := busySendChange(ctx, conn, `UPDATE busy_conversations SET publication = 'published', published_at_ms = ?, revision = revision + 1
	        WHERE conversation_id = ? AND revision = ? AND publication = 'candidate'`, at, conversation, candidateRevision); err != nil {
			return false, err
		}
	} else {
		if kind != string(BusySendLaunchResume) || conversation != p.conversation || (p.coverage == BusySendCovered && coverage != BusySendCovered) {
			return false, fmt.Errorf("store: invalid busy-send resume publication")
		}
		if err := busySendChange(ctx, conn, `UPDATE busy_conversations SET busy_send = ?,
	            default_send_mode = COALESCE(?, default_send_mode), default_confirmed = COALESCE(?, default_confirmed),
	            confirm_from_transcripts = COALESCE(?, confirm_from_transcripts), revision = revision + 1
	        WHERE conversation_id = ? AND revision = ? AND publication = 'published'`,
			string(coverage), mode, confirmed, fromTranscripts, conversation, p.conversationRevision); err != nil {
			return false, err
		}
	}
	if err := busySendChange(ctx, conn, `UPDATE busy_send_launches SET phase = 'superseded',
	    ended_at_ms = COALESCE(ended_at_ms, ?), end_cause = COALESCE(end_cause, 'launch_published'), revision = revision + 1
	    WHERE launch_id = ? AND revision = ? AND phase = 'current'`, at, p.launch, p.launchRevision); err != nil {
		return false, err
	}
	if err := busySendChange(ctx, conn, `UPDATE busy_send_launches SET phase = 'current', revision = revision + 1
	    WHERE launch_id = ? AND revision = ? AND phase IN ('candidate', 'uncertain')`, launch, revision); err != nil {
		return false, err
	}
	err = busySendChange(ctx, conn, `UPDATE spawns SET busy_send = ?, busy_conversation_id = ?
	    WHERE instance_id = ? AND launch_token IS ? AND busy_conversation_id = ?`, string(coverage), conversation,
		instanceID, nullableStringArg(token), p.conversation)
	return err == nil, err
}

// PublishBusySendLaunch handles a successful creation whose reply supplied no
// tmux identity. Labelled creations publish inside RecordLaunchIdentity. It
// advances the spawns version once only on promotion. version is retained in
// the interface for callers, but is not publication authority: adoption and
// hook writes can advance it without replacing this token's Launch.
func (s *Store) PublishBusySendLaunch(instanceID string, version int64, token string) (CondResult, error) {
	err := s.inImmediateTx(instanceID, func(ctx context.Context, conn *sql.Conn) error {
		var matches bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spawns WHERE instance_id = ? AND launch_token IS ?)`,
			instanceID, nullableStringArg(token)).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return errFamilyWriteNotApplied
		}
		published, err := publishBusySendCandidate(ctx, conn, instanceID, token, storeNowMillis())
		if err != nil || !published {
			return err
		}
		return advanceBusySendPublicationVersion(ctx, conn, instanceID, token)
	})
	if errors.Is(err, errFamilyWriteNotApplied) {
		return s.condNotApplied(instanceID, "store: publish busy-send Launch")
	}
	if err != nil {
		return 0, fmt.Errorf("store: publish busy-send Launch: %w", err)
	}
	return CondApplied, nil
}

func advanceBusySendPublicationVersion(ctx context.Context, conn busySendLaunchSQL, instanceID, token string) error {
	return busySendChange(ctx, conn, `UPDATE spawns SET `+rowVersionAdvance+` WHERE instance_id = ? AND launch_token IS ?`,
		instanceID, nullableStringArg(token))
}

// publishBusySendReportedLaunch is used only after an existing adoption or
// gated report-in write applied. That write pins the row/identity and already
// advances row_version, so publication adds no second version advance.
func publishBusySendReportedLaunch(ctx context.Context, conn busySendLaunchSQL, instanceID string) error {
	var token sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT launch_token FROM spawns WHERE instance_id = ?`, instanceID).Scan(&token); err != nil {
		return err
	}
	_, err := publishBusySendCandidate(ctx, conn, instanceID, token.String, storeNowMillis())
	return err
}

// execBusySendEstablished preserves a lifecycle write's original guards and
// publishes its candidate in the same transaction, only when that write lands.
func (s *Store) execBusySendEstablished(instanceID, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := s.inImmediateTx(instanceID, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		result, err = conn.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return errFamilyWriteNotApplied
		}
		return publishBusySendReportedLaunch(ctx, conn, instanceID)
	})
	if errors.Is(err, errFamilyWriteNotApplied) {
		err = nil
	}
	return result, err
}

// MarkBusySendLaunchUncertain retains a timed-out creation (or a failed
// publication/restore) for a later eligible lifecycle operation. It never
// changes the published conversation or another Launch's evidence.
func (s *Store) MarkBusySendLaunchUncertain(instanceID, token string) error {
	_, err := s.db.Exec(`UPDATE busy_send_launches SET phase = 'uncertain', revision = revision + 1
	    WHERE instance_id = ? AND launch_token = ? AND phase = 'candidate'
	        AND EXISTS (SELECT 1 FROM spawns s WHERE s.instance_id = ? AND s.launch_token = ?)`, instanceID, token, instanceID, token)
	if err != nil {
		return fmt.Errorf("store: mark busy-send Launch uncertain: %w", err)
	}
	return nil
}

// execBusySendRestore closes the candidate in the same transaction as the
// existing conditional restore. Published coverage/binding never changed, so
// restoring them would risk overwriting facts admitted during creation.
func (s *Store) execBusySendRestore(instanceID, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := s.inImmediateTx(instanceID, func(ctx context.Context, conn *sql.Conn) error {
		var token sql.NullString
		err := conn.QueryRowContext(ctx, `SELECT launch_token FROM spawns WHERE instance_id = ?`, instanceID).Scan(&token)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		result, err = conn.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return errFamilyWriteNotApplied
		}
		return closeBusySendCandidate(ctx, conn, instanceID, &token.String, BusySendLaunchFailed, BusySendCreationFailed, storeNowMillis())
	})
	if errors.Is(err, errFamilyWriteNotApplied) {
		err = nil
	}
	return result, err
}
