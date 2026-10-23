-- 0014_main_machine: the main machine is no longer the row 'main'. It is flagged, and the server gives it an
-- ID (mch_…) and a name from the computer's own name when it first starts (L2-010 MCH-01, L3-005 §10).
ALTER TABLE machines ADD COLUMN is_main INTEGER NOT NULL DEFAULT 0;
UPDATE machines SET is_main = 1 WHERE id = 'main';
CREATE UNIQUE INDEX machines_one_main ON machines(is_main) WHERE is_main = 1;
