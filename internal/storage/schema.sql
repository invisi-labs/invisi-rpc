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
