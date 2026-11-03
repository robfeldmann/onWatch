package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

// OMPProviderKey is the provider_accounts.provider value for broker accounts.
const OMPProviderKey = "omp"

// ompTimeLayout stores times with fixed millisecond precision so text order
// matches time order (RFC3339Nano trims trailing zeros, which breaks that).
const ompTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// ompMaxRangeRows bounds a range query when the caller passes no limit.
const ompMaxRangeRows = 5000

func formatOMPTime(t time.Time) string {
	return t.UTC().Format(ompTimeLayout)
}

func parseOMPTime(value, field string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid omp %s %q: %w", field, value, err)
	}
	return t.UTC(), nil
}

// OMPAccount is one broker account: a (source provider, identity) pair.
type OMPAccount struct {
	ID             int64
	Key            string // "<source provider>:<identity>", also provider_accounts.external_id
	SourceProvider string
	Identity       string
	Email          string
	AccountID      string // upstream account id from report metadata
	OrgID          string
	OrgName        string
	PlanType       string
	CreatedAt      time.Time
	LastFetchedAt  *time.Time // fetched_at of the newest stored snapshot
}

// ProviderLabel is the display name of the account's source provider.
func (a OMPAccount) ProviderLabel() string {
	return api.OMPProviderLabel(a.SourceProvider)
}

// Label is the account's display label, "<provider label> · <identity>",
// shared by the dashboard, the menubar, alerts and metrics.
func (a OMPAccount) Label() string {
	return a.ProviderLabel() + " · " + a.Identity
}

// GetOMPAccount returns one non-deleted omp account by id, or nil.
func (s *Store) GetOMPAccount(id int64) (*OMPAccount, error) {
	accounts, err := s.QueryOMPAccounts()
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i], nil
		}
	}
	return nil, nil
}

// ompAccountMetadata is the provider_accounts.metadata JSON of an omp account.
type ompAccountMetadata struct {
	SourceProvider string `json:"source_provider"`
	Identity       string `json:"identity"`
	Email          string `json:"email,omitempty"`
	AccountID      string `json:"account_id,omitempty"`
	OrgID          string `json:"org_id,omitempty"`
	OrgName        string `json:"org_name,omitempty"`
	PlanType       string `json:"plan_type,omitempty"`
}

// OMPSnapshot is one stored report of one account.
type OMPSnapshot struct {
	ID             int64
	AccountID      int64
	CapturedAt     time.Time // when onWatch polled the broker
	FetchedAt      time.Time // when the broker fetched the report upstream
	SourceProvider string
	Notes          []string
	Limits         []api.OMPLimit
}

// OMPLimitPoint is one stored value of a single limit.
type OMPLimitPoint struct {
	SnapshotID  int64
	FetchedAt   time.Time
	Used        *float64
	Limit       *float64
	Remaining   *float64
	Unit        string
	Utilization *float64
	Status      string
	ResetsAt    *time.Time
}

// OMPResetCycle is a reset cycle of one limit of one account.
type OMPResetCycle struct {
	ID              int64
	AccountID       int64
	LimitID         string
	CycleStart      time.Time
	CycleEnd        *time.Time
	ResetsAt        *time.Time
	PeakUtilization float64
	TotalDelta      float64
}

// UpsertOMPAccount returns the account row for a report, creating it on first
// sight and refreshing its metadata when the report's identity details change.
func (s *Store) UpsertOMPAccount(report api.OMPReport) (*OMPAccount, error) {
	key := report.AccountKey()
	acc, err := s.GetOrCreateProviderAccountByExternalID(OMPProviderKey, key, key)
	if err != nil {
		return nil, fmt.Errorf("omp account %s: %w", report.Provider, err)
	}
	meta := ompAccountMetadata{
		SourceProvider: report.Provider,
		Identity:       report.Identity,
		Email:          report.Email,
		AccountID:      report.AccountID,
		OrgID:          report.OrgID,
		OrgName:        report.OrgName,
		PlanType:       report.PlanType,
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("omp account metadata: %w", err)
	}
	if acc.Metadata != string(raw) {
		if err := s.UpdateProviderAccountMetadata(acc.ID, string(raw)); err != nil {
			return nil, err
		}
	}
	return &OMPAccount{
		ID:             acc.ID,
		Key:            key,
		SourceProvider: meta.SourceProvider,
		Identity:       meta.Identity,
		Email:          meta.Email,
		AccountID:      meta.AccountID,
		OrgID:          meta.OrgID,
		OrgName:        meta.OrgName,
		PlanType:       meta.PlanType,
		CreatedAt:      acc.CreatedAt,
	}, nil
}

// QueryOMPAccounts returns every non-deleted omp account, ordered by id, with
// the fetched_at of its newest snapshot.
func (s *Store) QueryOMPAccounts() ([]OMPAccount, error) {
	rows, err := s.db.Query(
		`SELECT pa.id, pa.name, COALESCE(pa.metadata, ''), pa.created_at,
			(SELECT MAX(fetched_at) FROM omp_snapshots os WHERE os.account_id = pa.id)
		FROM provider_accounts pa
		WHERE pa.provider = ? AND pa.deleted_at IS NULL
		ORDER BY pa.id`,
		OMPProviderKey,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query omp accounts: %w", err)
	}
	defer rows.Close()

	var accounts []OMPAccount
	for rows.Next() {
		var acc OMPAccount
		var metadata, createdAt string
		var lastFetched sql.NullString
		if err := rows.Scan(&acc.ID, &acc.Key, &metadata, &createdAt, &lastFetched); err != nil {
			return nil, fmt.Errorf("failed to scan omp account: %w", err)
		}
		acc.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		var meta ompAccountMetadata
		if metadata != "" {
			if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
				return nil, fmt.Errorf("invalid omp account %d metadata: %w", acc.ID, err)
			}
		}
		acc.SourceProvider = meta.SourceProvider
		acc.Identity = meta.Identity
		acc.Email = meta.Email
		acc.AccountID = meta.AccountID
		acc.OrgID = meta.OrgID
		acc.OrgName = meta.OrgName
		acc.PlanType = meta.PlanType
		if acc.SourceProvider == "" {
			// Metadata predates the account's first upsert; the key still
			// carries both parts.
			acc.SourceProvider, acc.Identity, _ = strings.Cut(acc.Key, ":")
		}
		if lastFetched.Valid {
			t, err := parseOMPTime(lastFetched.String, "fetched_at")
			if err != nil {
				return nil, err
			}
			acc.LastFetchedAt = &t
		}
		accounts = append(accounts, acc)
	}
	return accounts, rows.Err()
}

// InsertOMPSnapshot stores a report for an account unless a snapshot with the
// same fetched_at already exists (the broker served a cached report). It
// returns the new snapshot id and true, or 0 and false for a duplicate.
func (s *Store) InsertOMPSnapshot(accountID int64, capturedAt time.Time, report api.OMPReport) (int64, bool, error) {
	notes, err := json.Marshal(nonNilStrings(report.Notes))
	if err != nil {
		return 0, false, fmt.Errorf("omp snapshot notes: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("failed to begin omp snapshot transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`INSERT INTO omp_snapshots (account_id, captured_at, fetched_at, source_provider, notes, limit_count)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, fetched_at) DO NOTHING`,
		accountID, formatOMPTime(capturedAt), formatOMPTime(report.FetchedAt), report.Provider, string(notes), len(report.Limits),
	)
	if err != nil {
		return 0, false, fmt.Errorf("failed to insert omp snapshot: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return 0, false, fmt.Errorf("failed to read omp snapshot insert result: %w", err)
	} else if n == 0 {
		return 0, false, nil
	}
	snapshotID, err := result.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("failed to get omp snapshot id: %w", err)
	}

	stmt, err := tx.Prepare(
		`INSERT INTO omp_quota_values (snapshot_id, account_id, limit_id, label, window_id, window_label,
			window_duration_ms, used, limit_value, remaining, unit, utilization, status, resets_at, notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return 0, false, fmt.Errorf("failed to prepare omp quota insert: %w", err)
	}
	defer stmt.Close()

	for _, l := range report.Limits {
		limitNotes, err := json.Marshal(nonNilStrings(l.Notes))
		if err != nil {
			return 0, false, fmt.Errorf("omp limit notes: %w", err)
		}
		var duration, resetsAt interface{}
		if l.WindowDuration > 0 {
			duration = l.WindowDuration.Milliseconds()
		}
		if l.ResetsAt != nil {
			resetsAt = formatOMPTime(*l.ResetsAt)
		}
		if _, err := stmt.Exec(
			snapshotID, accountID, l.ID, l.Label, l.WindowID, l.WindowLabel,
			duration, nullableFloat(l.Used), nullableFloat(l.Limit), nullableFloat(l.Remaining),
			l.Unit, nullableFloat(l.Utilization), l.Status, resetsAt, string(limitNotes),
		); err != nil {
			return 0, false, fmt.Errorf("failed to insert omp quota value %s: %w", l.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("failed to commit omp snapshot: %w", err)
	}
	return snapshotID, true, nil
}

// QueryLatestOMPSnapshots returns the newest snapshot of every non-deleted
// account, ordered by account id.
func (s *Store) QueryLatestOMPSnapshots() ([]*OMPSnapshot, error) {
	return s.queryOMPSnapshots(
		`SELECT os.id, os.account_id, os.captured_at, os.fetched_at, os.source_provider, os.notes
		FROM omp_snapshots os
		JOIN provider_accounts pa ON pa.id = os.account_id AND pa.deleted_at IS NULL
		WHERE os.fetched_at = (SELECT MAX(fetched_at) FROM omp_snapshots WHERE account_id = os.account_id)
		ORDER BY os.account_id`,
	)
}

// QueryLatestOMPSnapshot returns the newest snapshot of an account, or nil.
func (s *Store) QueryLatestOMPSnapshot(accountID int64) (*OMPSnapshot, error) {
	snaps, err := s.queryOMPSnapshots(
		`SELECT id, account_id, captured_at, fetched_at, source_provider, notes
		FROM omp_snapshots WHERE account_id = ? ORDER BY fetched_at DESC LIMIT 1`,
		accountID,
	)
	if err != nil || len(snaps) == 0 {
		return nil, err
	}
	return snaps[0], nil
}

// QueryOMPRange returns an account's snapshots with fetched_at in [start, end),
// oldest first, capped at limit rows (ompMaxRangeRows when omitted).
func (s *Store) QueryOMPRange(accountID int64, start, end time.Time, limit ...int) ([]*OMPSnapshot, error) {
	max := ompMaxRangeRows
	if len(limit) > 0 && limit[0] > 0 {
		max = limit[0]
	}
	return s.queryOMPSnapshots(
		`SELECT id, account_id, captured_at, fetched_at, source_provider, notes FROM (
			SELECT id, account_id, captured_at, fetched_at, source_provider, notes
			FROM omp_snapshots WHERE account_id = ? AND fetched_at >= ? AND fetched_at < ?
			ORDER BY fetched_at DESC LIMIT ?
		) ORDER BY fetched_at ASC`,
		accountID, formatOMPTime(start), formatOMPTime(end), max,
	)
}

// QueryOMPLimitSeries returns one limit's values for an account with
// fetched_at in [start, end), oldest first, capped like QueryOMPRange.
func (s *Store) QueryOMPLimitSeries(accountID int64, limitID string, start, end time.Time, limit ...int) ([]OMPLimitPoint, error) {
	max := ompMaxRangeRows
	if len(limit) > 0 && limit[0] > 0 {
		max = limit[0]
	}
	rows, err := s.db.Query(
		`SELECT snapshot_id, fetched_at, used, limit_value, remaining, unit, utilization, status, resets_at FROM (
			SELECT qv.snapshot_id, os.fetched_at, qv.used, qv.limit_value, qv.remaining, qv.unit,
				qv.utilization, qv.status, qv.resets_at
			FROM omp_quota_values qv
			JOIN omp_snapshots os ON os.id = qv.snapshot_id
			WHERE qv.account_id = ? AND qv.limit_id = ? AND os.fetched_at >= ? AND os.fetched_at < ?
			ORDER BY os.fetched_at DESC LIMIT ?
		) ORDER BY fetched_at ASC`,
		accountID, limitID, formatOMPTime(start), formatOMPTime(end), max,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query omp limit series: %w", err)
	}
	defer rows.Close()

	var points []OMPLimitPoint
	for rows.Next() {
		var p OMPLimitPoint
		var fetchedAt string
		var used, limitValue, remaining, utilization sql.NullFloat64
		var resetsAt sql.NullString
		if err := rows.Scan(&p.SnapshotID, &fetchedAt, &used, &limitValue, &remaining, &p.Unit, &utilization, &p.Status, &resetsAt); err != nil {
			return nil, fmt.Errorf("failed to scan omp limit point: %w", err)
		}
		if p.FetchedAt, err = parseOMPTime(fetchedAt, "fetched_at"); err != nil {
			return nil, err
		}
		p.Used = floatPtr(used)
		p.Limit = floatPtr(limitValue)
		p.Remaining = floatPtr(remaining)
		p.Utilization = floatPtr(utilization)
		if resetsAt.Valid {
			t, err := parseOMPTime(resetsAt.String, "resets_at")
			if err != nil {
				return nil, err
			}
			p.ResetsAt = &t
		}
		points = append(points, p)
	}
	return points, rows.Err()
}

// QueryOMPLimitIDs returns the distinct limit ids ever stored for an account.
func (s *Store) QueryOMPLimitIDs(accountID int64) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT limit_id FROM omp_quota_values WHERE account_id = ? ORDER BY limit_id`,
		accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query omp limit ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan omp limit id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) queryOMPSnapshots(query string, args ...interface{}) ([]*OMPSnapshot, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query omp snapshots: %w", err)
	}
	var snapshots []*OMPSnapshot
	for rows.Next() {
		snap := &OMPSnapshot{}
		var capturedAt, fetchedAt, notes string
		if err := rows.Scan(&snap.ID, &snap.AccountID, &capturedAt, &fetchedAt, &snap.SourceProvider, &notes); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan omp snapshot: %w", err)
		}
		if snap.CapturedAt, err = parseOMPTime(capturedAt, "captured_at"); err != nil {
			rows.Close()
			return nil, err
		}
		if snap.FetchedAt, err = parseOMPTime(fetchedAt, "fetched_at"); err != nil {
			rows.Close()
			return nil, err
		}
		if snap.Notes, err = decodeOMPNotes(notes); err != nil {
			rows.Close()
			return nil, err
		}
		snapshots = append(snapshots, snap)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := s.attachOMPLimits(snapshots); err != nil {
		return nil, err
	}
	return snapshots, nil
}

// attachOMPLimits loads limit values for snapshots in batches of IN-list
// queries instead of one query per snapshot.
func (s *Store) attachOMPLimits(snapshots []*OMPSnapshot) error {
	const batchSize = 500
	byID := make(map[int64]*OMPSnapshot, len(snapshots))
	for _, snap := range snapshots {
		byID[snap.ID] = snap
		snap.Limits = []api.OMPLimit{}
	}
	for start := 0; start < len(snapshots); start += batchSize {
		end := min(start+batchSize, len(snapshots))
		args := make([]interface{}, 0, end-start)
		for _, snap := range snapshots[start:end] {
			args = append(args, snap.ID)
		}
		rows, err := s.db.Query(
			`SELECT snapshot_id, limit_id, label, window_id, window_label, window_duration_ms,
				used, limit_value, remaining, unit, utilization, status, resets_at, notes
			FROM omp_quota_values WHERE snapshot_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)
			ORDER BY snapshot_id, id`,
			args...,
		)
		if err != nil {
			return fmt.Errorf("failed to query omp quota values: %w", err)
		}
		for rows.Next() {
			var snapshotID int64
			var l api.OMPLimit
			var duration sql.NullInt64
			var used, limitValue, remaining, utilization sql.NullFloat64
			var resetsAt sql.NullString
			var notes string
			if err := rows.Scan(&snapshotID, &l.ID, &l.Label, &l.WindowID, &l.WindowLabel, &duration,
				&used, &limitValue, &remaining, &l.Unit, &utilization, &l.Status, &resetsAt, &notes); err != nil {
				rows.Close()
				return fmt.Errorf("failed to scan omp quota value: %w", err)
			}
			if duration.Valid {
				l.WindowDuration = time.Duration(duration.Int64) * time.Millisecond
			}
			l.Used = floatPtr(used)
			l.Limit = floatPtr(limitValue)
			l.Remaining = floatPtr(remaining)
			l.Utilization = floatPtr(utilization)
			if resetsAt.Valid {
				t, err := parseOMPTime(resetsAt.String, "resets_at")
				if err != nil {
					rows.Close()
					return err
				}
				l.ResetsAt = &t
			}
			if l.Notes, err = decodeOMPNotes(notes); err != nil {
				rows.Close()
				return err
			}
			if snap := byID[snapshotID]; snap != nil {
				snap.Limits = append(snap.Limits, l)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// CreateOMPCycle opens a reset cycle for one limit of an account.
func (s *Store) CreateOMPCycle(accountID int64, limitID string, cycleStart time.Time, resetsAt *time.Time) (int64, error) {
	result, err := s.db.Exec(
		`INSERT INTO omp_reset_cycles (account_id, limit_id, cycle_start, resets_at) VALUES (?, ?, ?, ?)`,
		accountID, limitID, formatOMPTime(cycleStart), nullableOMPTime(resetsAt),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create omp cycle: %w", err)
	}
	return result.LastInsertId()
}

// CloseOMPCycle closes the active cycle of a limit with its final stats.
func (s *Store) CloseOMPCycle(accountID int64, limitID string, cycleEnd time.Time, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE omp_reset_cycles SET cycle_end = ?, peak_utilization = ?, total_delta = ?
		WHERE account_id = ? AND limit_id = ? AND cycle_end IS NULL`,
		formatOMPTime(cycleEnd), peak, delta, accountID, limitID,
	)
	if err != nil {
		return fmt.Errorf("failed to close omp cycle: %w", err)
	}
	return nil
}

// UpdateOMPCycle updates the peak and delta of the active cycle of a limit.
func (s *Store) UpdateOMPCycle(accountID int64, limitID string, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE omp_reset_cycles SET peak_utilization = ?, total_delta = ?
		WHERE account_id = ? AND limit_id = ? AND cycle_end IS NULL`,
		peak, delta, accountID, limitID,
	)
	if err != nil {
		return fmt.Errorf("failed to update omp cycle: %w", err)
	}
	return nil
}

// UpdateOMPCycleResetsAt sets the reset time of the active cycle of a limit.
func (s *Store) UpdateOMPCycleResetsAt(accountID int64, limitID string, resetsAt *time.Time) error {
	_, err := s.db.Exec(
		`UPDATE omp_reset_cycles SET resets_at = ? WHERE account_id = ? AND limit_id = ? AND cycle_end IS NULL`,
		nullableOMPTime(resetsAt), accountID, limitID,
	)
	if err != nil {
		return fmt.Errorf("failed to update omp cycle resets_at: %w", err)
	}
	return nil
}

// QueryActiveOMPCycle returns the open cycle of a limit, or nil.
func (s *Store) QueryActiveOMPCycle(accountID int64, limitID string) (*OMPResetCycle, error) {
	cycles, err := s.queryOMPCycles(
		`SELECT id, account_id, limit_id, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM omp_reset_cycles WHERE account_id = ? AND limit_id = ? AND cycle_end IS NULL
		ORDER BY cycle_start DESC LIMIT 1`,
		accountID, limitID,
	)
	if err != nil || len(cycles) == 0 {
		return nil, err
	}
	return cycles[0], nil
}

// QueryOMPCycleHistory returns closed cycles of a limit, newest first, capped
// at limit rows (200 when omitted).
func (s *Store) QueryOMPCycleHistory(accountID int64, limitID string, limit ...int) ([]*OMPResetCycle, error) {
	max := 200
	if len(limit) > 0 && limit[0] > 0 {
		max = limit[0]
	}
	return s.queryOMPCycles(
		`SELECT id, account_id, limit_id, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM omp_reset_cycles WHERE account_id = ? AND limit_id = ? AND cycle_end IS NOT NULL
		ORDER BY cycle_start DESC LIMIT ?`,
		accountID, limitID, max,
	)
}

func (s *Store) queryOMPCycles(query string, args ...interface{}) ([]*OMPResetCycle, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query omp cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*OMPResetCycle
	for rows.Next() {
		var c OMPResetCycle
		var cycleStart string
		var cycleEnd, resetsAt sql.NullString
		if err := rows.Scan(&c.ID, &c.AccountID, &c.LimitID, &cycleStart, &cycleEnd, &resetsAt, &c.PeakUtilization, &c.TotalDelta); err != nil {
			return nil, fmt.Errorf("failed to scan omp cycle: %w", err)
		}
		if c.CycleStart, err = parseOMPTime(cycleStart, "cycle_start"); err != nil {
			return nil, err
		}
		if cycleEnd.Valid {
			t, err := parseOMPTime(cycleEnd.String, "cycle_end")
			if err != nil {
				return nil, err
			}
			c.CycleEnd = &t
		}
		if resetsAt.Valid {
			t, err := parseOMPTime(resetsAt.String, "cycle resets_at")
			if err != nil {
				return nil, err
			}
			c.ResetsAt = &t
		}
		cycles = append(cycles, &c)
	}
	return cycles, rows.Err()
}

func decodeOMPNotes(raw string) ([]string, error) {
	notes := []string{}
	if raw == "" {
		return notes, nil
	}
	if err := json.Unmarshal([]byte(raw), &notes); err != nil {
		return nil, fmt.Errorf("invalid omp notes %q: %w", raw, err)
	}
	return notes, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nullableFloat(v *float64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func nullableOMPTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return formatOMPTime(*t)
}

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}
