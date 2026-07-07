package db

import (
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/cali-go-stack/config"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// Init creates a PocketBase instance with ncruces/go-sqlite3 as the SQLite driver.
// ncruces is preferred over modernc for extension support (FTS5, spellfix1, unicode).
// Build with: go build -tags no_default_driver (optional, to exclude modernc binary size).
func Init(cfg *config.Config) (*pocketbase.PocketBase, error) {
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:       cfg.DataDir,
		DefaultEncryptionEnv: cfg.EncryptionKey,
		DBConnect: func(dbPath string) (*dbx.DB, error) {
			// Note: busy_timeout must be first so the connection blocks on busy
			// before WAL mode is set (in case another connection already set it).
			pragmas := "?_pragma=busy_timeout(10000)" +
				"&_pragma=journal_mode(WAL)" +
				"&_pragma=journal_size_limit(200000000)" +
				"&_pragma=synchronous(NORMAL)" +
				"&_pragma=foreign_keys(ON)" +
				"&_pragma=temp_store(MEMORY)" +
				"&_pragma=cache_size(-32000)"
			return dbx.Open("sqlite3", dbPath+pragmas)
		},
	})

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		return se.Next()
	})

	return app, nil
}
