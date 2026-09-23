package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// snapshotStore 实现 desired_snapshots（架构 §6.3/§10.1/ADR-2）：
// 单表按代唯一——generation 是身份、digest 是内容指纹；digest 普通索引，
// 同一内容可在多代出现（A→B→A），"回滚到第 N 代"是一次主键查找。
type snapshotStore struct {
	db *sql.DB
}

func NewSnapshotStore(db *DB) domain.SnapshotRepository {
	return &snapshotStore{db: db.sql}
}

func (s *snapshotStore) Materialize(ctx context.Context, snap *domain.DesiredStateSnapshot) (*domain.DesiredStateSnapshot, bool, error) {
	if snap.Machine == "" || snap.Digest == "" {
		return nil, false, fmt.Errorf("%w: snapshot requires machine and digest", domain.ErrInvalid)
	}
	// 快路径：与紧邻上一代内容相同 → 不新增代（FR-7.2：generation 仅在有效期望
	// 变化时递增），直接返回现行走。
	if lastGen, err := latestGenQuery(s.db, snap.Machine); err == nil {
		if cur, err := readSnapshotRow(ctx, s.db, snap.Machine, lastGen); err == nil && cur.Digest == snap.Digest {
			return cur, false, nil
		}
	}
	raw, err := snap.JSON()
	if err != nil {
		return nil, false, err
	}
	stamp := nowStamp()
	// 原子物化：单条 INSERT…SELECT 在 SQLite 写锁内完成"读上一代 → 比对 digest
	// → 分配 generation"三步（SELECT+INSERT 两步在并发下会重复分配代号）。
	// 影响行数为 0 表示并发窗口内上一代 digest 已与本代相同（他人先落）→ 复用。
	var changed bool
	err = withBusyRetry(ctx, "materialize_snapshot:"+snap.Machine, func() error {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO desired_snapshots
				(machine_id, generation, digest, canonicalization_version, snapshot, created_at)
			SELECT ?, COALESCE(
				(SELECT generation + 1 FROM desired_snapshots WHERE machine_id = ?
				 ORDER BY generation DESC LIMIT 1), 1),
			       ?, ?, ?, ?
			WHERE COALESCE((SELECT digest FROM desired_snapshots WHERE machine_id = ?
				 ORDER BY generation DESC LIMIT 1), '') != ?`,
			snap.Machine, snap.Machine,
			snap.Digest, snap.CanonicalizationVersion, string(raw), stamp,
			snap.Machine, snap.Digest)
		if err != nil {
			return fmt.Errorf("sqlite: materialize snapshot %q: %w", snap.Machine, err)
		}
		n, _ := res.RowsAffected()
		changed = n > 0
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if !changed {
		cur, err := s.Current(ctx, snap.Machine)
		if err != nil {
			return nil, false, err
		}
		return cur, false, nil
	}
	stored, err := s.Current(ctx, snap.Machine)
	if err != nil {
		return nil, false, err
	}
	snap.Generation = stored.Generation
	snap.CreatedAt = stored.CreatedAt
	return snap, true, nil
}

// latestGenQuery 取机器当前（最大 generation）代号；无代时返回 ErrNotFound。
func latestGenQuery(db *sql.DB, machine string) (int64, error) {
	var gen int64
	err := db.QueryRow(`SELECT generation FROM desired_snapshots WHERE machine_id = ?
		ORDER BY generation DESC LIMIT 1`, machine).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: snapshot for machine %q", domain.ErrNotFound, machine)
	}
	return gen, err
}

func (s *snapshotStore) Current(ctx context.Context, machine string) (*domain.DesiredStateSnapshot, error) {
	var gen int64
	err := s.db.QueryRowContext(ctx,
		`SELECT generation FROM desired_snapshots WHERE machine_id = ?
		 ORDER BY generation DESC LIMIT 1`, machine).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: snapshot for machine %q", domain.ErrNotFound, machine)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: read current generation for %q: %w", machine, err)
	}
	return s.Get(ctx, machine, gen)
}

func (s *snapshotStore) Get(ctx context.Context, machine string, generation int64) (*domain.DesiredStateSnapshot, error) {
	row, err := readSnapshotRow(ctx, s.db, machine, generation)
	if err != nil {
		return nil, err
	}
	return row, nil
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readSnapshotRow(ctx context.Context, q rowQuerier, machine string, generation int64) (*domain.DesiredStateSnapshot, error) {
	var digest, canon, raw, createdAt string
	err := q.QueryRowContext(ctx,
		`SELECT digest, canonicalization_version, snapshot, created_at
		 FROM desired_snapshots WHERE machine_id = ? AND generation = ?`, machine, generation).
		Scan(&digest, &canon, &raw, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: snapshot %q gen %d", domain.ErrNotFound, machine, generation)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: read snapshot %q gen %d: %w", machine, generation, err)
	}
	snap, err := domain.ParseSnapshot([]byte(raw))
	if err != nil {
		return nil, err
	}
	// 列为准（防 JSON 与列漂移）：身份列与内容列必须一致。
	snap.Machine = machine
	snap.Generation = generation
	snap.Digest = digest
	snap.CanonicalizationVersion = canon
	if t, err := parseTime(createdAt, "snapshot.created_at"); err == nil {
		snap.CreatedAt = t
	}
	return snap, nil
}
