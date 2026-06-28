package flags

import "strings"

const (
	DatabaseTypeSQLite   = "sqlite"
	DatabaseTypePostgres = "postgres"
)

var (
	// 数据库配置
	DatabaseType string // 数据库类型：sqlite
	DatabaseFile string // SQLite数据库文件路径
	DatabaseHost string // 保留的兼容参数，当前未使用
	DatabasePort string // 保留的兼容参数，当前未使用
	DatabaseUser string // 保留的兼容参数，当前未使用
	DatabasePass string // 保留的兼容参数，当前未使用
	DatabaseName string // 保留的兼容参数，当前未使用

	Listen string
)

func NormalizeDatabaseType(databaseType string) string {
	databaseType = strings.ToLower(strings.TrimSpace(databaseType))
	if databaseType == "" {
		return DatabaseTypeSQLite
	}
	return databaseType
}

func ApplyDatabaseTypeNormalization() string {
	DatabaseType = NormalizeDatabaseType(DatabaseType)
	return DatabaseType
}

func IsSQLite() bool {
	return NormalizeDatabaseType(DatabaseType) == DatabaseTypeSQLite
}

func SupportedDatabaseTypes() string {
	return DatabaseTypeSQLite + ", " + DatabaseTypePostgres
}

func IsPostgres() bool {
	return NormalizeDatabaseType(DatabaseType) == DatabaseTypePostgres
}

// BuildPostgresDSN 根据 flags 中的 DatabaseHost/Port/User/Pass/Name 构建 PostgreSQL DSN
func BuildPostgresDSN() string {
	// 使用 environment variables or flags
	host := DatabaseHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := DatabasePort
	if port == "" {
		port = "5432"
	}
	dsn := "host=" + host + " port=" + port
	if DatabaseUser != "" {
		dsn += " user=" + DatabaseUser
	}
	if DatabasePass != "" {
		dsn += " password=" + DatabasePass
	}
	if DatabaseName != "" {
		dsn += " dbname=" + DatabaseName
	}
	dsn += " sslmode=disable TimeZone=UTC"
	return dsn
}
