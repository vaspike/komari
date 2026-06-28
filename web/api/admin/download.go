package admin

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/web/api"
)

// copyFile 复制单个文件到目标路径（会确保父目录存在）
func copyFile(srcPath, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("failed to create parent directory: %v", err)
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source file: %v", err)
	}
	defer src.Close()

	dest, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %v", err)
	}
	defer dest.Close()

	if _, err = io.Copy(dest, src); err != nil {
		return fmt.Errorf("failed to copy file: %v", err)
	}
	return nil
}

// copyDataToTempExcludingDB 将 ./data 下除了 .db/.db-wal/.db-shm 之外的所有文件复制到临时目录
func copyDataToTempExcludingDB(tempDir string) error {
	dataRoot := "./data"

	// 如果 data 目录不存在，视为无文件可复制
	if stat, err := os.Stat(dataRoot); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat data dir: %v", err)
	} else if !stat.IsDir() {
		return fmt.Errorf("%s is not a directory", dataRoot)
	}

	return filepath.Walk(dataRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dataRoot, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		// 跳过数据库相关文件
		name := info.Name()
		if strings.HasSuffix(strings.ToLower(name), ".db") ||
			strings.HasSuffix(strings.ToLower(name), ".db-wal") ||
			strings.HasSuffix(strings.ToLower(name), ".db-shm") {
			return nil
		}

		dst := filepath.Join(tempDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		return copyFile(p, dst)
	})
}

// backupSQLiteTo 使用 SQLite VACUUM INTO 将当前数据库一致性备份到指定路径
func backupSQLiteTo(destDBPath string) error {
	if err := os.MkdirAll(filepath.Dir(destDBPath), 0o755); err != nil {
		return fmt.Errorf("failed to create parent directory for db: %v", err)
	}

	db := dbcore.GetDBInstance()
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get underlying database connection: %v", err)
	}

	safePath := strings.ReplaceAll(destDBPath, "'", "''")
	vacuumSQL := fmt.Sprintf("VACUUM INTO '%s'", safePath)
	if _, err = sqlDB.Exec(vacuumSQL); err != nil {
		return fmt.Errorf("sqlite VACUUM INTO failed: %v", err)
	}
	return nil
}

// backupPostgresTo 使用 pg_dump 将 PostgreSQL 数据库导出为 SQL 文件
func backupPostgresTo(destSQLPath string) error {
	if err := os.MkdirAll(filepath.Dir(destSQLPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent directory for pg dump: %v", err)
	}

	// pg_dump uses PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE env vars
	cmd := exec.Command("pg_dump",
		"--no-owner",
		"--no-privileges",
		"--inserts",
		"--file", destSQLPath,
	)
	// Pass connection info via environment from the DSN
	cmd.Env = append(os.Environ(),
		"PGHOST="+flags.DatabaseHost,
		"PGPORT="+flags.DatabasePort,
		"PGUSER="+flags.DatabaseUser,
		"PGPASSWORD="+flags.DatabasePass,
		"PGDATABASE="+flags.DatabaseName,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_dump failed: %v\nOutput: %s", err, string(output))
	}
	return nil
}

// DownloadBackup 用于打包 ./data 目录及数据库文件为 zip 并通过 HTTP 下载
func DownloadBackup(c *gin.Context) {
	// 1) 创建临时目录
	tempDir, err := os.MkdirTemp("", "komari-backup-*")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating temporary directory: %v", err))
		return
	}
	defer os.RemoveAll(tempDir)

	// 2) 复制 ./data 下除 .db/.db-wal/.db-shm 外的所有文件到临时目录
	if err := copyDataToTempExcludingDB(tempDir); err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error copying data to temp: %v", err))
		return
	}

	// 3) 处理数据库备份
	dbType := flags.ApplyDatabaseTypeNormalization()

	if dbType == flags.DatabaseTypeSQLite {
		// SQLite: VACUUM INTO 导出到 komari.db
		destDB := filepath.Join(tempDir, "komari.db")
		if err := backupSQLiteTo(destDB); err != nil {
			api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error backing up sqlite database: %v", err))
			return
		}
	} else if dbType == flags.DatabaseTypePostgres {
		// PostgreSQL: 使用 pg_dump 导出到 komari.sql
		destSQL := filepath.Join(tempDir, "komari.sql")
		if err := backupPostgresTo(destSQL); err != nil {
			api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error backing up postgres database: %v", err))
			return
		}
	} else {
		dbFilePath := flags.DatabaseFile
		if dbFilePath != "" {
			destDB := filepath.Join(tempDir, "komari.db")
			if _, err := os.Stat(dbFilePath); err == nil {
				if err := copyFile(dbFilePath, destDB); err != nil {
					api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error copying database file: %v", err))
					return
				}
			} else if !os.IsNotExist(err) {
				api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error stating database file: %v", err))
				return
			}
		}
	}

	// 写入数据库类型标记文件
	dbTypeMarkup := filepath.Join(tempDir, "db-type.txt")
	os.WriteFile(dbTypeMarkup, []byte(dbType), 0644)

	// 4) 开始写出 ZIP（以临时目录为根）
	backupFileName := fmt.Sprintf("backup-%d.zip", time.Now().UnixMicro())
	c.Writer.Header().Set("Content-Type", "application/zip")
	c.Writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", backupFileName))

	zipWriter := zip.NewWriter(c.Writer)
	defer zipWriter.Close()

	// 写入临时目录里的内容
	err = filepath.Walk(tempDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tempDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// zip 内路径统一正斜杠
		zipPath := filepath.ToSlash(rel)
		if info.IsDir() {
			_, err := zipWriter.CreateHeader(&zip.FileHeader{
				Name:     zipPath + "/",
				Method:   zip.Deflate,
				Modified: info.ModTime(),
			})
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		w, err := zipWriter.CreateHeader(&zip.FileHeader{
			Name:     zipPath,
			Method:   zip.Deflate,
			Modified: info.ModTime(),
		})
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		return err
	})
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error archiving temp folder: %v", err))
		return
	}

	// 5) 追加备份标记文件（放在 zip 根目录）
	markupContent := "此文件为 Komari 备份标记文件，请勿删除。\nThis is a Komari backup markup file, please do not delete.\n\n备份时间 / Backup Time: " + time.Now().Format(time.RFC3339)
	markupWriter, err := zipWriter.CreateHeader(&zip.FileHeader{
		Name:     "komari-backup-markup",
		Method:   zip.Deflate,
		Modified: time.Now(),
	})
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating backup markup file: %v", err))
		return
	}
	if _, err = markupWriter.Write([]byte(markupContent)); err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error writing backup markup file: %v", err))
		return
	}
}
