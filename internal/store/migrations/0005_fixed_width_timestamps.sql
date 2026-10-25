-- 0005_fixed_width_timestamps: every stored time becomes RFC 3339 UTC with exactly nine fractional digits,
-- so text comparison equals time comparison (see tsLayout in store.go). Older rows were written with
-- RFC3339Nano, which drops trailing zeros: '…05Z', '…05.1Z', '…05.123456789Z'. All values are UTC ('Z').

UPDATE users SET
  created_at = CASE WHEN created_at IS NULL OR length(created_at) = 30 THEN created_at WHEN instr(created_at, '.') = 0 THEN substr(created_at, 1, 19) || '.000000000Z' ELSE substr(created_at, 1, 20) || substr(substr(created_at, 21, length(created_at) - 21) || '000000000', 1, 9) || 'Z' END;

UPDATE sessions SET
  created_at = CASE WHEN created_at IS NULL OR length(created_at) = 30 THEN created_at WHEN instr(created_at, '.') = 0 THEN substr(created_at, 1, 19) || '.000000000Z' ELSE substr(created_at, 1, 20) || substr(substr(created_at, 21, length(created_at) - 21) || '000000000', 1, 9) || 'Z' END,
  expires_at = CASE WHEN expires_at IS NULL OR length(expires_at) = 30 THEN expires_at WHEN instr(expires_at, '.') = 0 THEN substr(expires_at, 1, 19) || '.000000000Z' ELSE substr(expires_at, 1, 20) || substr(substr(expires_at, 21, length(expires_at) - 21) || '000000000', 1, 9) || 'Z' END,
  last_seen_at = CASE WHEN last_seen_at IS NULL OR length(last_seen_at) = 30 THEN last_seen_at WHEN instr(last_seen_at, '.') = 0 THEN substr(last_seen_at, 1, 19) || '.000000000Z' ELSE substr(last_seen_at, 1, 20) || substr(substr(last_seen_at, 21, length(last_seen_at) - 21) || '000000000', 1, 9) || 'Z' END;

UPDATE api_tokens SET
  created_at = CASE WHEN created_at IS NULL OR length(created_at) = 30 THEN created_at WHEN instr(created_at, '.') = 0 THEN substr(created_at, 1, 19) || '.000000000Z' ELSE substr(created_at, 1, 20) || substr(substr(created_at, 21, length(created_at) - 21) || '000000000', 1, 9) || 'Z' END,
  last_used_at = CASE WHEN last_used_at IS NULL OR length(last_used_at) = 30 THEN last_used_at WHEN instr(last_used_at, '.') = 0 THEN substr(last_used_at, 1, 19) || '.000000000Z' ELSE substr(last_used_at, 1, 20) || substr(substr(last_used_at, 21, length(last_used_at) - 21) || '000000000', 1, 9) || 'Z' END,
  expires_at = CASE WHEN expires_at IS NULL OR length(expires_at) = 30 THEN expires_at WHEN instr(expires_at, '.') = 0 THEN substr(expires_at, 1, 19) || '.000000000Z' ELSE substr(expires_at, 1, 20) || substr(substr(expires_at, 21, length(expires_at) - 21) || '000000000', 1, 9) || 'Z' END,
  revoked_at = CASE WHEN revoked_at IS NULL OR length(revoked_at) = 30 THEN revoked_at WHEN instr(revoked_at, '.') = 0 THEN substr(revoked_at, 1, 19) || '.000000000Z' ELSE substr(revoked_at, 1, 20) || substr(substr(revoked_at, 21, length(revoked_at) - 21) || '000000000', 1, 9) || 'Z' END;

UPDATE resources SET
  next_attempt_at = CASE WHEN next_attempt_at IS NULL OR length(next_attempt_at) = 30 THEN next_attempt_at WHEN instr(next_attempt_at, '.') = 0 THEN substr(next_attempt_at, 1, 19) || '.000000000Z' ELSE substr(next_attempt_at, 1, 20) || substr(substr(next_attempt_at, 21, length(next_attempt_at) - 21) || '000000000', 1, 9) || 'Z' END,
  last_transition_at = CASE WHEN last_transition_at IS NULL OR length(last_transition_at) = 30 THEN last_transition_at WHEN instr(last_transition_at, '.') = 0 THEN substr(last_transition_at, 1, 19) || '.000000000Z' ELSE substr(last_transition_at, 1, 20) || substr(substr(last_transition_at, 21, length(last_transition_at) - 21) || '000000000', 1, 9) || 'Z' END,
  created_at = CASE WHEN created_at IS NULL OR length(created_at) = 30 THEN created_at WHEN instr(created_at, '.') = 0 THEN substr(created_at, 1, 19) || '.000000000Z' ELSE substr(created_at, 1, 20) || substr(substr(created_at, 21, length(created_at) - 21) || '000000000', 1, 9) || 'Z' END,
  updated_at = CASE WHEN updated_at IS NULL OR length(updated_at) = 30 THEN updated_at WHEN instr(updated_at, '.') = 0 THEN substr(updated_at, 1, 19) || '.000000000Z' ELSE substr(updated_at, 1, 20) || substr(substr(updated_at, 21, length(updated_at) - 21) || '000000000', 1, 9) || 'Z' END,
  next_run_at = CASE WHEN next_run_at IS NULL OR length(next_run_at) = 30 THEN next_run_at WHEN instr(next_run_at, '.') = 0 THEN substr(next_run_at, 1, 19) || '.000000000Z' ELSE substr(next_run_at, 1, 20) || substr(substr(next_run_at, 21, length(next_run_at) - 21) || '000000000', 1, 9) || 'Z' END;

UPDATE resource_events SET
  created_at = CASE WHEN created_at IS NULL OR length(created_at) = 30 THEN created_at WHEN instr(created_at, '.') = 0 THEN substr(created_at, 1, 19) || '.000000000Z' ELSE substr(created_at, 1, 20) || substr(substr(created_at, 21, length(created_at) - 21) || '000000000', 1, 9) || 'Z' END;

UPDATE idempotency_keys SET
  created_at = CASE WHEN created_at IS NULL OR length(created_at) = 30 THEN created_at WHEN instr(created_at, '.') = 0 THEN substr(created_at, 1, 19) || '.000000000Z' ELSE substr(created_at, 1, 20) || substr(substr(created_at, 21, length(created_at) - 21) || '000000000', 1, 9) || 'Z' END;

UPDATE runs SET
  scheduled_for = CASE WHEN scheduled_for IS NULL OR length(scheduled_for) = 30 THEN scheduled_for WHEN instr(scheduled_for, '.') = 0 THEN substr(scheduled_for, 1, 19) || '.000000000Z' ELSE substr(scheduled_for, 1, 20) || substr(substr(scheduled_for, 21, length(scheduled_for) - 21) || '000000000', 1, 9) || 'Z' END,
  started_at = CASE WHEN started_at IS NULL OR length(started_at) = 30 THEN started_at WHEN instr(started_at, '.') = 0 THEN substr(started_at, 1, 19) || '.000000000Z' ELSE substr(started_at, 1, 20) || substr(substr(started_at, 21, length(started_at) - 21) || '000000000', 1, 9) || 'Z' END,
  finished_at = CASE WHEN finished_at IS NULL OR length(finished_at) = 30 THEN finished_at WHEN instr(finished_at, '.') = 0 THEN substr(finished_at, 1, 19) || '.000000000Z' ELSE substr(finished_at, 1, 20) || substr(substr(finished_at, 21, length(finished_at) - 21) || '000000000', 1, 9) || 'Z' END;

UPDATE schema_migrations SET
  applied_at = CASE WHEN applied_at IS NULL OR length(applied_at) = 30 THEN applied_at WHEN instr(applied_at, '.') = 0 THEN substr(applied_at, 1, 19) || '.000000000Z' ELSE substr(applied_at, 1, 20) || substr(substr(applied_at, 21, length(applied_at) - 21) || '000000000', 1, 9) || 'Z' END;
