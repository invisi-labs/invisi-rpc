-- PostgreSQL and SQLite: additive, deployment-scoped operator safety state.
CREATE TABLE IF NOT EXISTS invisi_finalized_anchors (
 deployment text PRIMARY KEY,
 block text NOT NULL,
 conflicted integer NOT NULL DEFAULT 0
);
