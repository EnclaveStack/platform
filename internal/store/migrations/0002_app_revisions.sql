-- 0002_app_revisions: the revision of an app that receives traffic (ADR-016, L3-005 §5). 0 = none yet,
-- which also covers v0.2 apps: their single container is adopted as the serving revision on the next deploy.
ALTER TABLE resources ADD COLUMN serving_revision INTEGER NOT NULL DEFAULT 0;
