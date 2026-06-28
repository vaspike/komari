package cmd

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/spf13/cobra"
	"gorm.io/driver/postgres"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var (
	migrateFromSQLiteFile string
)

var MigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate data from SQLite to PostgreSQL",
	Long: `Migrate all data from a SQLite database (or backup.zip) into a PostgreSQL database.

The target PostgreSQL connection is configured via the standard database flags
(--db-host, --db-port, --db-user, --db-pass, --db-name) or environment variables
(KOMARI_DB_HOST, KOMARI_DB_PORT, KOMARI_DB_USER, KOMARI_DB_PASS, KOMARI_DB_NAME).

Supports:
  - A .db SQLite file directly
  - A Komari backup.zip file (extracts komari.db and theme files from it)

Examples:
  komari migrate --from-sqlite backup.zip
  komari migrate --from-sqlite /path/to/komari.db`,
	Run: runMigrate,
}

func init() {
	MigrateCmd.PersistentFlags().StringVar(&migrateFromSQLiteFile, "from-sqlite", "", "Path to SQLite database file (.db) or Komari backup.zip [env: KOMARI_MIGRATE_FROM]")
	RootCmd.AddCommand(MigrateCmd)
}

func runMigrate(cmd *cobra.Command, args []string) {
	if migrateFromSQLiteFile == "" {
		migrateFromSQLiteFile = GetEnv("KOMARI_MIGRATE_FROM", "")
	}
	if migrateFromSQLiteFile == "" {
		log.Fatal("--from-sqlite is required. Usage: komari migrate --from-sqlite <file.db|backup.zip>")
	}

	// Step 1: Determine source (zip or direct db)
	dbPath := migrateFromSQLiteFile
	tempDir := ""
	if strings.HasSuffix(strings.ToLower(migrateFromSQLiteFile), ".zip") {
		var err error
		tempDir, err = extractBackupZip(migrateFromSQLiteFile)
		if err != nil {
			log.Fatalf("Failed to extract backup zip: %v", err)
		}
		defer os.RemoveAll(tempDir)
		dbPath = filepath.Join(tempDir, "data", "komari.db")
		if _, err := os.Stat(dbPath); err != nil {
			// Also try root of zip
			dbPath = filepath.Join(tempDir, "komari.db")
			if _, err := os.Stat(dbPath); err != nil {
				log.Fatal("Backup zip does not contain komari.db")
			}
		}
	}

	// Step 2: Open SQLite source
	srcDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to open SQLite database: %v", err)
	}
	log.Printf("Opened SQLite source: %s", dbPath)

	// Step 3: Connect to PostgreSQL target
	dsn := flags.BuildPostgresDSN()
	tgtDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	log.Printf("Connected to PostgreSQL: %s@%s:%s/%s", flags.DatabaseUser, flags.DatabaseHost, flags.DatabasePort, flags.DatabaseName)

	// Step 4: AutoMigrate all tables in PG
	log.Println("Creating tables in PostgreSQL...")
	if err := tgtDB.AutoMigrate(
		&config.ConfigItem{},
		&models.User{},
		&models.Client{},
		&models.Record{},
		&models.GPURecord{},
		&models.Log{},
		&models.Clipboard{},
		&models.LoadNotification{},
		&models.OfflineNotification{},
		&models.TrafficReportNotification{},
		&models.PingRecord{},
		&models.PingTask{},
		&models.OidcProvider{},
		&models.MessageSenderProvider{},
		&models.ThemeConfiguration{},
		&models.Session{},
		&models.Task{},
		&models.TaskResult{},
	); err != nil {
		log.Fatalf("Failed to create tables: %v", err)
	}
	// Also create long_term tables
	if err := tgtDB.Table("records_long_term").AutoMigrate(&models.Record{}); err != nil {
		log.Printf("Failed to create records_long_term: %v (may already exist)", err)
	}
	if err := tgtDB.Table("gpu_records_long_term").AutoMigrate(&models.GPURecord{}); err != nil {
		log.Printf("Failed to create gpu_records_long_term: %v (may already exist)", err)
	}

	// Create indexes
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_record_client_time ON records(client, time)",
		"CREATE INDEX IF NOT EXISTS idx_record_lt_client_time ON records_long_term(client, time)",
		"CREATE INDEX IF NOT EXISTS idx_gpu_record_client_time ON gpu_records(client, time)",
		"CREATE INDEX IF NOT EXISTS idx_gpu_record_lt_client_time ON gpu_records_long_term(client, time)",
		"CREATE INDEX IF NOT EXISTS idx_ping_record_client_time ON ping_records(client, time)",
	}
	for _, idx := range indexes {
		if err := tgtDB.Exec(idx).Error; err != nil {
			log.Printf("Warning: failed to create index: %v", err)
		}
	}

	// Step 5: Migrate data in dependency order
	migrateTable(srcDB, tgtDB, "configs", &config.ConfigItem{})
	migrateTable(srcDB, tgtDB, "users", &models.User{})
	migrateTable(srcDB, tgtDB, "clients", &models.Client{})

	// Large tables - use batch insert
	migrateTableBatch(srcDB, tgtDB, "records", &models.Record{}, 500)
	migrateTableBatch(srcDB, tgtDB, "records_long_term", &models.Record{}, 500)
	migrateTableBatch(srcDB, tgtDB, "gpu_records", &models.GPURecord{}, 500)
	migrateTableBatch(srcDB, tgtDB, "gpu_records_long_term", &models.GPURecord{}, 500)

	// Remaining tables
	migrateTable(srcDB, tgtDB, "logs", &models.Log{})
	migrateTable(srcDB, tgtDB, "clipboards", &models.Clipboard{})
	migrateTable(srcDB, tgtDB, "load_notifications", &models.LoadNotification{})
	migrateTable(srcDB, tgtDB, "offline_notifications", &models.OfflineNotification{})
	migrateTable(srcDB, tgtDB, "traffic_report_notifications", &models.TrafficReportNotification{})
	migrateTable(srcDB, tgtDB, "ping_records", &models.PingRecord{})
	migrateTable(srcDB, tgtDB, "ping_tasks", &models.PingTask{})
	migrateTable(srcDB, tgtDB, "oidc_providers", &models.OidcProvider{})
	migrateTable(srcDB, tgtDB, "message_sender_providers", &models.MessageSenderProvider{})
	migrateTable(srcDB, tgtDB, "theme_configurations", &models.ThemeConfiguration{})
	migrateTable(srcDB, tgtDB, "sessions", &models.Session{})
	migrateTable(srcDB, tgtDB, "tasks", &models.Task{})
	migrateTable(srcDB, tgtDB, "task_results", &models.TaskResult{})

	// Step 6: Copy theme files from backup to ./data/theme
	if tempDir != "" {
		copyThemeFiles(tempDir)
	}

	log.Println("Migration completed successfully.")
}

// migrateTable reads all rows from src into a slice of model, then inserts into tgt.
func migrateTable[T any](src, tgt *gorm.DB, tableName string, model *T) {
	var rows []T
	if err := src.Table(tableName).Find(&rows).Error; err != nil {
		log.Printf("Warning: failed to read from %s: %v", tableName, err)
		return
	}
	if len(rows) == 0 {
		log.Printf("Table %s: 0 rows (empty)", tableName)
		return
	}
	if err := tgt.Table(tableName).Create(&rows).Error; err != nil {
		log.Fatalf("Failed to insert into %s: %v", tableName, err)
	}
	log.Printf("Table %s: %d rows migrated", tableName, len(rows))
}

// migrateTableBatch reads and inserts in batches for large tables.
func migrateTableBatch[T any](src, tgt *gorm.DB, tableName string, model *T, batchSize int) {
	var count int64
	src.Table(tableName).Count(&count)
	if count == 0 {
		log.Printf("Table %s: 0 rows (empty)", tableName)
		return
	}

	var totalMigrated int64
	for offset := int64(0); offset < count; offset += int64(batchSize) {
		var rows []T
		if err := src.Table(tableName).Offset(int(offset)).Limit(batchSize).Find(&rows).Error; err != nil {
			log.Fatalf("Failed to read from %s at offset %d: %v", tableName, offset, err)
		}
		if len(rows) == 0 {
			break
		}
		if err := tgt.Table(tableName).Create(&rows).Error; err != nil {
			log.Fatalf("Failed to insert into %s at offset %d: %v", tableName, offset, err)
		}
		totalMigrated += int64(len(rows))
		log.Printf("Table %s: %d/%d rows migrated", tableName, totalMigrated, count)
	}
}

// extractBackupZip extracts a backup zip to a temp directory.
func extractBackupZip(zipPath string) (string, error) {
	tempDir, err := os.MkdirTemp("", "komari-migrate-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %v", err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		os.RemoveAll(tempDir)
		return "", fmt.Errorf("failed to open zip: %v", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		cleanName := filepath.Clean(f.Name)
		targetPath := filepath.Join(tempDir, cleanName)

		// Path traversal protection
		absTemp, _ := filepath.Abs(tempDir)
		if !strings.HasPrefix(targetPath, absTemp+string(os.PathSeparator)) && targetPath != absTemp {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(targetPath, 0755)
			continue
		}

		os.MkdirAll(filepath.Dir(targetPath), 0755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(targetPath)
		if err != nil {
			rc.Close()
			continue
		}
		io.Copy(out, rc)
		out.Close()
		rc.Close()
	}
	return tempDir, nil
}

// copyThemeFiles copies theme data from extracted backup to ./data/theme.
func copyThemeFiles(tempDir string) {
	themeSrc := filepath.Join(tempDir, "data", "theme")
	if _, err := os.Stat(themeSrc); err != nil {
		// Try root-level theme
		themeSrc = filepath.Join(tempDir, "theme")
		if _, err := os.Stat(themeSrc); err != nil {
			log.Println("No theme files found in backup, skipping")
			return
		}
	}

	destDir := "./data/theme"
	if err := os.MkdirAll(destDir, os.ModePerm); err != nil {
		log.Printf("Warning: failed to create theme dir: %v", err)
		return
	}

	err := filepath.Walk(themeSrc, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(themeSrc, path)
		if rel == "." {
			return nil
		}
		dst := filepath.Join(destDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, src)
		return err
	})
	if err != nil {
		log.Printf("Warning: error copying theme files: %v", err)
	} else {
		log.Println("Theme files copied successfully")
	}
}
