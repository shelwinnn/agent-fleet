package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/ids"
)

// objectOf 是泛型仓储的类型参数约束：T 为资源结构体，PT 为其指针类型。
type objectOf[T any] interface {
	*T
	domain.ResourceObject
}

// resourceStore 是同构资源表的通用仓储（profiles/providers/skills/deployments 直接复用；
// machines 因带查询列单独实现）。表名来自代码内常量，无注入面。
type resourceStore[T any, PT objectOf[T]] struct {
	db    *sql.DB
	table string
}

func (s *resourceStore[T, PT]) Create(ctx context.Context, obj PT) error {
	meta := obj.Meta()
	if meta.Name == "" {
		return fmt.Errorf("%w: metadata.name is required", domain.ErrInvalid)
	}
	if len(obj.SpecJSON()) == 0 {
		obj.SetSpecJSON(json.RawMessage("{}"))
	}
	if len(obj.StatusJSON()) == 0 {
		obj.SetStatusJSON(json.RawMessage("{}"))
	}
	meta.UID = ids.NewUID()
	meta.ResourceVersion = 1
	now := time.Now().UTC()
	meta.CreationTimestamp = now
	stamp := now.Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (id, name, spec, status, resource_version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`, s.table),
		meta.UID, meta.Name, string(obj.SpecJSON()), string(obj.StatusJSON()),
		meta.ResourceVersion, stamp, stamp)
	return mapConstraintErr(err, s.table)
}

func (s *resourceStore[T, PT]) Get(ctx context.Context, name string) (PT, error) {
	var id, resourceName, spec, status, createdAt, updatedAt string
	var rv int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT id, name, spec, status, resource_version, created_at, updated_at
		 FROM %s WHERE name = ?`, s.table), name).
		Scan(&id, &resourceName, &spec, &status, &rv, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s %q", domain.ErrNotFound, s.table, name)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get %s %q: %w", s.table, name, err)
	}
	obj := PT(new(T))
	if err := fillMeta(obj.Meta(), id, resourceName, rv, createdAt); err != nil {
		return nil, err
	}
	obj.SetSpecJSON(json.RawMessage(spec))
	obj.SetStatusJSON(json.RawMessage(status))
	return obj, nil
}

func (s *resourceStore[T, PT]) List(ctx context.Context) ([]PT, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, name, spec, status, resource_version, created_at, updated_at
		 FROM %s ORDER BY name`, s.table))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list %s: %w", s.table, err)
	}
	defer rows.Close()

	var out []PT
	for rows.Next() {
		var id, name, spec, status, createdAt, updatedAt string
		var rv int64
		if err := rows.Scan(&id, &name, &spec, &status, &rv, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan %s: %w", s.table, err)
		}
		obj := PT(new(T))
		if err := fillMeta(obj.Meta(), id, name, rv, createdAt); err != nil {
			return nil, err
		}
		obj.SetSpecJSON(json.RawMessage(spec))
		obj.SetStatusJSON(json.RawMessage(status))
		out = append(out, obj)
	}
	return out, rows.Err()
}

func (s *resourceStore[T, PT]) Update(ctx context.Context, obj PT) error {
	meta := obj.Meta()
	if meta.Name == "" {
		return fmt.Errorf("%w: metadata.name is required", domain.ErrInvalid)
	}
	if len(obj.SpecJSON()) == 0 {
		obj.SetSpecJSON(json.RawMessage("{}"))
	}
	if len(obj.StatusJSON()) == 0 {
		obj.SetStatusJSON(json.RawMessage("{}"))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin update %s: %w", s.table, err)
	}
	defer func() { _ = tx.Rollback() }()

	var rv int64
	err = tx.QueryRowContext(ctx,
		`SELECT resource_version FROM `+s.table+` WHERE name = ?`, meta.Name).Scan(&rv)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %q", domain.ErrNotFound, s.table, meta.Name)
	}
	if err != nil {
		return fmt.Errorf("sqlite: lock %s %q: %w", s.table, meta.Name, err)
	}
	meta.ResourceVersion = rv + 1
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET spec = ?, status = ?, resource_version = ?, updated_at = ?
		 WHERE name = ?`, s.table),
		string(obj.SpecJSON()), string(obj.StatusJSON()), meta.ResourceVersion,
		time.Now().UTC().Format(time.RFC3339Nano), meta.Name); err != nil {
		return fmt.Errorf("sqlite: update %s %q: %w", s.table, meta.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit update %s: %w", s.table, err)
	}
	return nil
}

func (s *resourceStore[T, PT]) Delete(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("sqlite: delete %s %q: %w", s.table, name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s %q", domain.ErrNotFound, s.table, name)
	}
	return nil
}

func fillMeta(meta *domain.ObjectMeta, id, name string, rv int64, createdAt string) error {
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return fmt.Errorf("sqlite: parse created_at %q: %w", createdAt, err)
	}
	meta.UID, meta.Name, meta.ResourceVersion, meta.CreationTimestamp = id, name, rv, created
	return nil
}
