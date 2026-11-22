package model

import "errors"

// These sentinels are shared by finality readers and transaction guards so callers
// can recheck native finality only after the database transaction has ended.
var ErrFinalityConflict = errors.New("accepted finalized history conflict; operator recovery required")
var ErrFinalizedRollback = errors.New("rollback would remove protected finalized history; retry")
