package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ---- CredentialTXStore 实现 --------------------------------------------------

func (s *PGStore) LockEnvelopeForUpdate(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID) (*CredentialEnvelope, error) {
	const q = `SELECT workspace_id, secret_ref, version, ciphertext, data_nonce,
		wrapped_dek, wrap_nonce, envelope_suite, kek_version,
		created_at, retired_at FROM credential_envelopes
		WHERE workspace_id = $1 AND secret_ref = $2
		ORDER BY version DESC LIMIT 1 FOR UPDATE`
	env := &CredentialEnvelope{}
	err := tx.QueryRowContext(ctx, q, wsID, secretRef).Scan(
		&env.WorkspaceID, &env.SecretRef, &env.Version,
		&env.Ciphertext, &env.DataNonce, &env.WrappedDEK, &env.WrapNonce,
		&env.EnvelopeSuite, &env.KEKVersion,
		&env.CreatedAt, &env.RetiredAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("credential envelope (%s, %s): %w", wsID, secretRef, ErrEnvelopeNotFound)
	}
	if err != nil {
		return nil, err
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
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("credential envelope (%s, %s, %d): %w", wsID, secretRef, version, ErrEnvelopeNotFound)
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
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf(
			"credential envelope (%s, %s, %d): active row not updated",
			wsID,
			secretRef,
			version,
		)
	}
	return nil
}

// ---- ConnectionTXStore 实现 --------------------------------------------------

func (s *PGStore) UpdateConnectionVersion(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, newVersion int) error {
	const q = `UPDATE connections SET
			secret_version = $1,
			updated_at = GREATEST(clock_timestamp(), updated_at + interval '1 microsecond')
		WHERE workspace_id = $2 AND secret_ref = $3`
	_, err := tx.ExecContext(ctx, q, newVersion, wsID, secretRef)
	return err
}

func (s *PGStore) CountConnectionsByVersion(ctx context.Context, tx *sql.Tx, wsID, secretRef uuid.UUID, version int) (int, error) {
	const q = `SELECT id FROM connections
		WHERE workspace_id = $1 AND secret_ref = $2 AND secret_version = $3
		FOR SHARE`
	rows, err := tx.QueryContext(ctx, q, wsID, secretRef, version)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}
