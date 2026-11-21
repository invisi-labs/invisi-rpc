CREATE TABLE IF NOT EXISTS invisi_blocks (
 deployment text NOT NULL, number bigint NOT NULL, hash bytea NOT NULL, parent_hash bytea NOT NULL, batch jsonb NOT NULL,
 PRIMARY KEY(deployment,number)
);
CREATE TABLE IF NOT EXISTS invisi_trees (
 deployment text NOT NULL, tree_id bigint NOT NULL, block_number bigint NOT NULL,
 PRIMARY KEY(deployment,tree_id), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invisi_roots (
 deployment text NOT NULL, tree_id bigint NOT NULL, leaf_count bigint NOT NULL, root bytea NOT NULL, block_number bigint NOT NULL, sequence bigint NOT NULL,
 PRIMARY KEY(deployment,tree_id,leaf_count), FOREIGN KEY(deployment,tree_id) REFERENCES invisi_trees ON DELETE CASCADE,
 FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS invisi_roots_lookup ON invisi_roots(deployment,tree_id,root);
CREATE TABLE IF NOT EXISTS invisi_nodes (
 deployment text NOT NULL, tree_id bigint NOT NULL, level integer NOT NULL, node_index bigint NOT NULL, leaf_count bigint NOT NULL, hash bytea NOT NULL, block_number bigint NOT NULL,
 PRIMARY KEY(deployment,tree_id,level,node_index,leaf_count), FOREIGN KEY(deployment,tree_id) REFERENCES invisi_trees ON DELETE CASCADE,
 FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invisi_nullifiers (
 deployment text NOT NULL, hash bytea NOT NULL, block_number bigint NOT NULL,
 PRIMARY KEY(deployment,hash), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invisi_deployments (
 deployment text PRIMARY KEY, from_block bigint NOT NULL, runtime_code_hash bytea NOT NULL
);

CREATE TABLE IF NOT EXISTS invisi_index_formats (deployment text PRIMARY KEY, version integer NOT NULL);

CREATE TABLE IF NOT EXISTS invisi_registrations (
 deployment text NOT NULL, owner bytea NOT NULL, block_number bigint NOT NULL, log_index bigint NOT NULL, registration jsonb NOT NULL,
 PRIMARY KEY(deployment,owner,block_number,log_index), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invisi_registry_profiles (deployment text PRIMARY KEY, profile text NOT NULL);

CREATE TABLE IF NOT EXISTS invisi_broadcasters (
 deployment text NOT NULL, operator bytea NOT NULL, block_number bigint NOT NULL, log_index bigint NOT NULL, kind text NOT NULL, value jsonb NOT NULL,
 PRIMARY KEY(deployment,operator,block_number,log_index), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS invisi_broadcaster_profiles (deployment text PRIMARY KEY, profile text NOT NULL);

CREATE TABLE IF NOT EXISTS invisi_broadcaster_formats (deployment text PRIMARY KEY, version integer NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS invisi_broadcaster_positions ON invisi_broadcasters(deployment,block_number,log_index);

CREATE INDEX IF NOT EXISTS invisi_commitment_lookup ON invisi_nodes(deployment,hash) WHERE level=0;

CREATE TABLE IF NOT EXISTS invisi_credit_nullifiers (
 deployment text NOT NULL, hash bytea NOT NULL, block_number bigint NOT NULL,
 PRIMARY KEY(deployment,hash), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS invisi_credit_status_events (
 deployment text NOT NULL, owner bytea NOT NULL, token bytea NOT NULL, block_number bigint NOT NULL, log_index bigint NOT NULL,
 kind text NOT NULL, grant_id text NOT NULL, status integer NOT NULL CHECK(status IN (1,2)),
 PRIMARY KEY(deployment,block_number,log_index), FOREIGN KEY(deployment,block_number) REFERENCES invisi_blocks ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS invisi_credit_status_lookup ON invisi_credit_status_events(deployment,owner,token,grant_id,block_number,log_index);

CREATE TABLE IF NOT EXISTS invisi_finalized_anchors (deployment text PRIMARY KEY, block text NOT NULL, conflicted integer NOT NULL DEFAULT 0);
