package db

import "time"

// nowRFC3339 timestamps schema_migrations rows. It is kept local to the package
// so the migration runner has no dependency on higher-level services.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
