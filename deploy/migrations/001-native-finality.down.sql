-- Stop the service and retain a backup before rollback: this drops the durable
-- accepted-finality safety fence. Re-upgrading requires operator revalidation.
DROP TABLE IF EXISTS invisi_finalized_anchors;
