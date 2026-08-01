package metadata

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// ---- CredentialTXStore 实现 --------------------------------------------------

func (s *PGStore) LockEnvelopeForUpdate(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID) (*CredentialEnvelope, error) {
	const q = `SELECT workspace_id, secret_ref, version, ciphertext, data_nonce,
		wrapped_dek, wrap_nonce, envelope_suite, kek_version,
		created_at, retired_at FROM credential_envelopes
		WHERE workspace_id = $1 AND secret_ref = $2 FOR UPDATE`
	rows, err := tx.QueryContext(ctx, q, wsID, secretRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var env *CredentialEnvelope
	var maxVersion int
	for rows.Next() {
		var e CredentialEnvelope
		if err := rows.Scan(
			&e.WorkspaceID, &e.SecretRef, &e.Version,
			&e.Ciphertext, &e.DataNonce, &e.WrappedDEK, &e.WrapNonce,
			&e.EnvelopeSuite, &e.KEKVersion,
			&e.CreatedAt, &e.RetiredAt,
		); err != nil {
			return nil, err
		}
		if e.Version > maxVersion {
			maxVersion = e.Version
			copied := e
			env = &copied
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if env == nil {
		return nil, fmt.Errorf("credential envelope (%s, %s): not found", wsID, secretRef)
	}
	return env, nil
}

func (s *PGStore) LockEnvelopeVersion(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, version int) (*CredentialEnvelope, error) {
	const q = `SELECT workspace_id, secret_ref, version, ciphertext, data_nonce,
		wrapped_dek, wrap_nonce, envelope_suite, kek_version,
		created_at, retired_at FROM credential_envelopes
		WHERE workspace_id = $1 AND secret_ref = $2 AND version = $3 FOR UPDATE`
	env := &CredentialEnvelope{}
	err := tx.QueryRowContext(ctx, q, wsID, secretRef, version).Scan(
		&env.WorkspaceID, &env.SecretRef, &env.Version,
		&env.Ciphertext, &env.DataNonce, &env.WrappedDEK, &env.WrapNonce,
		&env.EnvelopeSuite, &env.KEKVersion,
		&env.CreatedAt, &env.RetiredAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("credential envelope (%s, %s, %d): not found", wsID, secretRef, version)
	}
	if err != nil {
		return nil, err
	}
	return env, nil
}

func (s *PGStore) InsertEnvelopeTx(ctx context.Context, tx *sql.Tx, env *CredentialEnvelope) error {
	const q = `
		INSERT INTO credential_envelopes
			(workspace_id, secret_ref, version, ciphertext, data_nonce,
			 wrapped_dek, wrap_nonce, envelope_suite, kek_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING created_at`
	return tx.QueryRowContext(ctx, q,
		env.WorkspaceID, env.SecretRef, env.Version,
		env.Ciphertext, env.DataNonce, env.WrappedDEK, env.WrapNonce,
		env.EnvelopeSuite, env.KEKVersion,
	).Scan(&env.CreatedAt)
}

func (s *PGStore) ListEnvelopesByRef(ctx context.Context, wsID, secretRef uuid.UUID) ([]CredentialEnvelope, error) {
	const q = `SELECT workspace_id, secret_ref, version, ciphertext, data_nonce,
		wrapped_dek, wrap_nonce, envelope_suite, kek_version,
		created_at, retired_at FROM credential_envelopes
		WHERE workspace_id = $1 AND secret_ref = $2 ORDER BY version DESC`
	rows, err := s.DB.QueryContext(ctx, q, wsID, secretRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var envs []CredentialEnvelope
	for rows.Next() {
		var env CredentialEnvelope
		if err := rows.Scan(
			&env.WorkspaceID, &env.SecretRef, &env.Version,
			&env.Ciphertext, &env.DataNonce, &env.WrappedDEK, &env.WrapNonce,
			&env.EnvelopeSuite, &env.KEKVersion,
			&env.CreatedAt, &env.RetiredAt,
		); err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}
	return envs, rows.Err()
}

func (s *PGStore) UpdateRetiredAt(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, version int) error {
	const q = `UPDATE credential_envelopes SET retired_at = now()
		WHERE workspace_id = $1 AND secret_ref = $2 AND version = $3 AND retired_at IS NULL`
	res, err := tx.ExecContext(ctx, q, wsID, secretRef, version)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil // 幂等：已退役则静默成功
	}
	return nil
}

// ---- ConnectionTXStore 实现 --------------------------------------------------

func (s *PGStore) UpdateConnectionVersion(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, newVersion int) error {
	const q = `UPDATE connections SET secret_version = $1, updated_at = now()
		WHERE workspace_id = $2 AND secret_ref = $3`
	_, err := tx.ExecContext(ctx, q, newVersion, wsID, secretRef)
	return err
}

func (s *PGStore) CountConnectionsByVersion(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, version int) (int, error) {
	const q = `SELECT COUNT(*) FROM connections
		WHERE workspace_id = $1 AND secret_ref = $2 AND secret_version = $3`
	var count int
	err := tx.QueryRowContext(ctx, q, wsID, secretRef, version).Scan(&count)
	return count, err
}
