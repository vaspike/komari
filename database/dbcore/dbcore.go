package dbcore

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/komari-monitor/komari/pkg/migrations"
	logutil "github.com/komari-monitor/komari/utils/log"
	"os/exec"

	"gorm.io/driver/postgres"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// zipDirectoryExcluding 将 srcDir 打包为 dstZip，exclude 是绝对路径集合需要排除
func zipDirectoryExcluding(srcDir, dstZip string, exclude map[string]struct{}) error {
	// 规范化排除路径为绝对路径
	normExclude := make(map[string]struct{}, len(exclude))
	for p := range exclude {
		abs, _ := filepath.Abs(p)
		normExclude[abs] = struct{}{}
	}

	out, err := os.Create(dstZip)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	absSrc, _ := filepath.Abs(srcDir)
	walkErr := filepath.Walk(absSrc, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// 排除 backup.zip 本身
		if _, ok := normExclude[path]; ok {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// 计算 zip 内相对路径
		rel, err := filepath.Rel(absSrc, path)
		if err != nil {
			return err
		}
		// 根目录跳过
		if rel == "." {
			return nil
		}
		// 替换为正斜杠
		zipName := filepath.ToSlash(rel)

		if info.IsDir() {
			_, err := zw.Create(zipName + "/")
			return err
		}
		// 普通文件
		fh, err := os.Open(path)
		if err != nil {
			return err
		}
		w, err := zw.Create(zipName)
		if err != nil {
			fh.Close()
			return err
		}
		if _, err := io.Copy(w, fh); err != nil {
			fh.Close()
			return err
		}
		fh.Close()
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	return zw.Close()
}

// removeAllInDirExcept 删除 dir 下除 exclude 指定绝对路径外的所有文件和文件夹
func removeAllInDirExcept(dir string, exclude map[string]struct{}) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	normExclude := make(map[string]struct{}, len(exclude))
	for p := range exclude {
		abs, _ := filepath.Abs(p)
		normExclude[abs] = struct{}{}
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := filepath.Join(absDir, e.Name())
		if _, ok := normExclude[full]; ok {
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			return err
		}
	}
	return nil
}

// unzipToDir 将 zipPath 解压到 dstDir，包含路径遍历保护
func unzipToDir(zipPath, dstDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	absDst, _ := filepath.Abs(dstDir)

	for _, f := range zr.File {
		// 构造目标路径并做路径遍历保护
		cleanName := filepath.Clean(f.Name)
		targetPath := filepath.Join(absDst, cleanName)
		if !strings.HasPrefix(targetPath, absDst+string(os.PathSeparator)) && targetPath != absDst {
			return fmt.Errorf("illegal file path in zip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(targetPath)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

var (
	instance *gorm.DB
	once     sync.Once
)

func GetDBInstance() *gorm.DB {
	once.Do(func() {

		var err error

		// 在数据库初始化前执行：如果存在 ./data/backup.zip，则进行恢复逻辑
		restoreBackupZip()

		logConfig := &gorm.Config{
			Logger: logutil.NewGormLogger(),
		}

		// 根据数据库类型选择不同的连接方式
		switch flags.ApplyDatabaseTypeNormalization() {
		case flags.DatabaseTypeSQLite:
			// SQLite 连接
			instance, err = gorm.Open(sqlite.Open(flags.DatabaseFile), logConfig)
			if err != nil {
				log.Fatalf("Failed to connect to SQLite3 database: %v", err)
			}
			log.Printf("Using SQLite database file: %s", flags.DatabaseFile)
			instance.Exec("PRAGMA journal_mode = WAL;")
			instance.Exec("PRAGMA synchronous = NORMAL;")
			instance.Exec("PRAGMA cache_size = -65536;")
			instance.Exec("PRAGMA temp_store = MEMORY;")
			instance.Exec("PRAGMA wal_checkpoint(TRUNCATE);")
		case flags.DatabaseTypePostgres:
			// PostgreSQL 连接
			dsn := flags.BuildPostgresDSN()
			instance, err = gorm.Open(postgres.Open(dsn), logConfig)
			if err != nil {
				log.Fatalf("Failed to connect to PostgreSQL database: %v", err)
			}
			log.Printf("Using PostgreSQL database: %s@%s:%s/%s", flags.DatabaseUser, flags.DatabaseHost, flags.DatabasePort, flags.DatabaseName)
		default:
			log.Fatalf("Unsupported database type: %s (supported: %s)", flags.DatabaseType, flags.SupportedDatabaseTypes())
		}
		if err := migrations.Run(migrations.Context{DB: instance}); err != nil {
			log.Fatalf("Failed to run startup migrations: %v", err)
		}
		config.SetDb(instance)
		// 自动迁移模型
		err = instance.AutoMigrate(
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
		)
		if err != nil {
			log.Fatalf("Failed to create tables: %v", err)
		}
		err = instance.Table("records_long_term").AutoMigrate(
			&models.Record{},
		)
		if err != nil {
			log.Printf("Failed to create records_long_term table, it may already exist: %v", err)
		}
		err = instance.Table("gpu_records_long_term").AutoMigrate(
			&models.GPURecord{},
		)
		if err != nil {
			log.Printf("Failed to create gpu_records_long_term table, it may already exist: %v", err)
		}
		err = instance.AutoMigrate(
			&models.Session{},
		)
		if err != nil {
			log.Printf("Failed to create Session table, it may already exist: %v", err)
		}
		err = instance.AutoMigrate(
			&models.Task{},
			&models.TaskResult{},
		)
		if err != nil {
			log.Printf("Failed to create Task and TaskResult table, it may already exist: %v", err)
		}

		// Manually create composite indexes
		if flags.IsSQLite() || flags.IsPostgres() {
			instance.Exec("CREATE INDEX IF NOT EXISTS idx_record_client_time ON records(client, time)")
			instance.Exec("CREATE INDEX IF NOT EXISTS idx_record_lt_client_time ON records_long_term(client, time)")
			instance.Exec("CREATE INDEX IF NOT EXISTS idx_gpu_record_client_time ON gpu_records(client, time)")
			instance.Exec("CREATE INDEX IF NOT EXISTS idx_gpu_record_lt_client_time ON gpu_records_long_term(client, time)")
			instance.Exec("CREATE INDEX IF NOT EXISTS idx_ping_record_client_time ON ping_records(client, time)")
		}

	})

	return instance
}

// restoreBackupZip 处理 ./data/backup.zip 的恢复逻辑。
// 支持 SQLite → SQLite、PG → PG、以及 SQLite 备份 → PG 的自动迁移。
func restoreBackupZip() {
	backupZipPath := filepath.Join(".", "data", "backup.zip")
	if _, statErr := os.Stat(backupZipPath); statErr != nil {
		return
	}

	dbType := flags.ApplyDatabaseTypeNormalization()

	// 读取备份类型标记
	backupType := ""
	if zr, zipErr := zip.OpenReader(backupZipPath); zipErr == nil {
		for _, f := range zr.File {
			if f.Name == "db-type.txt" {
				rc, _ := f.Open()
				buf := make([]byte, 32)
				n, _ := rc.Read(buf)
				backupType = strings.TrimSpace(string(buf[:n]))
				rc.Close()
				break
			}
		}
		zr.Close()
	}
	if backupType == "" {
		// 旧版备份没有 db-type.txt，默认为 sqlite
		backupType = flags.DatabaseTypeSQLite
	}

	// 创建安全备份
	if err := os.MkdirAll("./backup", 0755); err != nil {
		log.Printf("[restore] failed to create backup dir: %v", err)
	} else {
		tsName := time.Now().Format("20060102-150405")
		bakPath := filepath.Join("./backup", fmt.Sprintf("%s.zip", tsName))
		if zipErr := zipDirectoryExcluding("./data", bakPath, map[string]struct{}{backupZipPath: {}}); zipErr != nil {
			log.Printf("[restore] failed to zip current data: %v", zipErr)
		} else {
			log.Printf("[restore] current data zipped to %s", bakPath)
		}
	}

	cleanup := true
	if backupType == flags.DatabaseTypeSQLite && dbType == flags.DatabaseTypeSQLite {
		// SQLite → SQLite：原有行为，解压覆盖
		restoreSQLiteBackup(backupZipPath)
	} else if backupType == flags.DatabaseTypePostgres && dbType == flags.DatabaseTypePostgres {
		// PG → PG：提取 SQL dump，用 psql 恢复
		restorePostgresBackup(backupZipPath)
	} else if backupType == flags.DatabaseTypeSQLite && dbType == flags.DatabaseTypePostgres {
		// SQLite → PG：自动迁移
		cleanup = restoreSQLiteToPostgres(backupZipPath)
	} else {
		log.Printf("[restore] Unsupported backup type migration: %s → %s", backupType, dbType)
	}

	if cleanup {
		os.Remove(backupZipPath)
		os.Remove("./data/komari-backup-markup")
		log.Printf("[restore] backup.zip removed, restore complete")
	}
}

func restoreSQLiteBackup(backupZipPath string) {
	if delErr := removeAllInDirExcept("./data", map[string]struct{}{backupZipPath: {}}); delErr != nil {
		log.Printf("[restore] failed to cleanup data dir: %v", delErr)
	}
	if unzipErr := unzipToDir(backupZipPath, "./data"); unzipErr != nil {
		log.Printf("[restore] failed to unzip backup: %v", unzipErr)
	} else {
		log.Printf("[restore] backup.zip extracted to ./data")
	}
}

func restorePostgresBackup(backupZipPath string) {
	// 解压到临时目录，找到 komari.sql，用 psql 导入
	tempDir, err := os.MkdirTemp("", "komari-pg-restore-*")
	if err != nil {
		log.Printf("[restore] failed to create temp dir: %v", err)
		return
	}
	defer os.RemoveAll(tempDir)

	if err := unzipToDir(backupZipPath, tempDir); err != nil {
		log.Printf("[restore] failed to extract backup: %v", err)
		return
	}

	sqlPath := filepath.Join(tempDir, "komari.sql")
	if _, err := os.Stat(sqlPath); err != nil {
		// Try nested data/komari.sql
		sqlPath = filepath.Join(tempDir, "data", "komari.sql")
		if _, err := os.Stat(sqlPath); err != nil {
			log.Printf("[restore] backup does not contain komari.sql")
			return
		}
	}

	cmd := exec.Command("psql", "-f", sqlPath)
	cmd.Env = append(os.Environ(),
		"PGHOST="+flags.DatabaseHost,
		"PGPORT="+flags.DatabasePort,
		"PGUSER="+flags.DatabaseUser,
		"PGPASSWORD="+flags.DatabasePass,
		"PGDATABASE="+flags.DatabaseName,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[restore] psql restore failed: %v\nOutput: %s", err, string(output))
	} else {
		log.Printf("[restore] PostgreSQL database restored from backup")
	}

	// 恢复主题文件
	copyThemeFromBackup(tempDir)
}

func restoreSQLiteToPostgres(backupZipPath string) bool {
	log.Printf("[restore] SQLite backup detected on PostgreSQL mode, auto-migrating...")

	// 解压到临时目录
	tempDir, err := os.MkdirTemp("", "komari-sql2pg-*")
	if err != nil {
		log.Printf("[restore] failed to create temp dir: %v", err)
		return true
	}
	defer os.RemoveAll(tempDir)

	if err := unzipToDir(backupZipPath, tempDir); err != nil {
		log.Printf("[restore] failed to extract backup: %v", err)
		return true
	}

	dbPath := filepath.Join(tempDir, "komari.db")
	if _, err := os.Stat(dbPath); err != nil {
		dbPath = filepath.Join(tempDir, "data", "komari.db")
		if _, err := os.Stat(dbPath); err != nil {
			log.Printf("[restore] backup does not contain komari.db, cannot auto-migrate")
			return true
		}
	}

	// 打开 SQLite
	srcDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		log.Printf("[restore] failed to open SQLite: %v", err)
		return true
	}

	// 连接 PG
	dsn := flags.BuildPostgresDSN()
	tgtDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Printf("[restore] failed to connect to PostgreSQL: %v", err)
		return true
	}
	log.Printf("[restore] connected to PostgreSQL, starting migration...")

	// AutoMigrate on PG
	tgtDB.AutoMigrate(
		&config.ConfigItem{},
		&models.User{}, &models.Client{},
		&models.Record{}, &models.GPURecord{},
		&models.Log{}, &models.Clipboard{},
		&models.LoadNotification{}, &models.OfflineNotification{},
		&models.TrafficReportNotification{},
		&models.PingRecord{}, &models.PingTask{},
		&models.OidcProvider{}, &models.MessageSenderProvider{},
		&models.ThemeConfiguration{},
		&models.Session{}, &models.Task{}, &models.TaskResult{},
	)
	tgtDB.Table("records_long_term").AutoMigrate(&models.Record{})
	tgtDB.Table("gpu_records_long_term").AutoMigrate(&models.GPURecord{})

	// 迁移数据 (精简版，批量处理大表)
	migrateTable(srcDB, tgtDB, "configs", &config.ConfigItem{})
	migrateTable(srcDB, tgtDB, "users", &models.User{})
	migrateTable(srcDB, tgtDB, "clients", &models.Client{})

	batchMigrate(srcDB, tgtDB, "records", &models.Record{}, 500)
	batchMigrate(srcDB, tgtDB, "records_long_term", &models.Record{}, 500)
	batchMigrate(srcDB, tgtDB, "gpu_records", &models.GPURecord{}, 500)
	batchMigrate(srcDB, tgtDB, "gpu_records_long_term", &models.GPURecord{}, 500)

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

	// 复制主题文件
	copyThemeFromBackup(tempDir)

	log.Printf("[restore] auto-migration from SQLite to PostgreSQL completed")
	return true
}

// migrateTable 批量复制 (dbcore 内部使用)
func migrateTable[T any](src, tgt *gorm.DB, table string, _ *T) {
	var rows []T
	if err := src.Table(table).Find(&rows).Error; err != nil {
		log.Printf("[restore] read %s: %v", table, err)
		return
	}
	if len(rows) == 0 {
		return
	}
	if err := tgt.Table(table).Create(&rows).Error; err != nil {
		log.Printf("[restore] insert %s: %v", table, err)
		return
	}
	log.Printf("[restore] %s: %d rows", table, len(rows))
}

// batchMigrate 大表分批迁移
func batchMigrate[T any](src, tgt *gorm.DB, table string, _ *T, batchSize int) {
	var count int64
	src.Table(table).Count(&count)
	if count == 0 {
		return
	}
	var migrated int64
	for offset := int64(0); offset < count; offset += int64(batchSize) {
		var rows []T
		src.Table(table).Offset(int(offset)).Limit(batchSize).Find(&rows)
		if len(rows) == 0 {
			break
		}
		tgt.Table(table).Create(&rows)
		migrated += int64(len(rows))
	}
	log.Printf("[restore] %s: %d/%d rows", table, migrated, count)
}

func copyThemeFromBackup(tempDir string) {
	themeSrc := filepath.Join(tempDir, "data", "theme")
	if _, err := os.Stat(themeSrc); err != nil {
		themeSrc = filepath.Join(tempDir, "theme")
		if _, err := os.Stat(themeSrc); err != nil {
			return
		}
	}
	destDir := "./data/theme"
	os.MkdirAll(destDir, os.ModePerm)
	filepath.Walk(themeSrc, func(path string, info os.FileInfo, err error) error {
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
		src, _ := os.Open(path)
		if src != nil {
			defer src.Close()
			out, _ := os.Create(dst)
			if out != nil {
				defer out.Close()
				io.Copy(out, src)
			}
		}
		return nil
	})
}
