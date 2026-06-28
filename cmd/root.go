package cmd

import (
	"fmt"
	"os"

	"github.com/komari-monitor/komari/cmd/flags"

	"github.com/spf13/cobra"
)

func GetEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// 从环境变量获取默认值
var (
	dbTypeEnv = GetEnv("KOMARI_DB_TYPE", "sqlite")
	dbFileEnv = GetEnv("KOMARI_DB_FILE", "./data/komari.db")
	dbHostEnv = GetEnv("KOMARI_DB_HOST", "localhost")
	dbPortEnv = GetEnv("KOMARI_DB_PORT", "5432")
	dbUserEnv = GetEnv("KOMARI_DB_USER", "root")
	dbPassEnv = GetEnv("KOMARI_DB_PASS", "")
	dbNameEnv = GetEnv("KOMARI_DB_NAME", "komari")
)

var RootCmd = &cobra.Command{
	Use:   "Komari",
	Short: "Komari is a simple server monitoring tool",
	Long: `Komari is a simple server monitoring tool. 
Made by Akizon77 with love.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.SetArgs([]string{"server"})
		cmd.Execute()
	},
}

func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// 设置命令行参数，提供环境变量作为默认值
	RootCmd.PersistentFlags().StringVarP(&flags.DatabaseType, "db-type", "t", dbTypeEnv, "Database type (sqlite, postgres) [env: KOMARI_DB_TYPE]")
	RootCmd.PersistentFlags().StringVarP(&flags.DatabaseFile, "database", "d", dbFileEnv, "SQLite database file path [env: KOMARI_DB_FILE]")
	RootCmd.PersistentFlags().StringVar(&flags.DatabaseHost, "db-host", dbHostEnv, "Database host (for postgres) [env: KOMARI_DB_HOST]")
	RootCmd.PersistentFlags().StringVar(&flags.DatabasePort, "db-port", dbPortEnv, "Database port (for postgres) [env: KOMARI_DB_PORT]")
	RootCmd.PersistentFlags().StringVar(&flags.DatabaseUser, "db-user", dbUserEnv, "Database user (for postgres) [env: KOMARI_DB_USER]")
	RootCmd.PersistentFlags().StringVar(&flags.DatabasePass, "db-pass", dbPassEnv, "Database password (for postgres) [env: KOMARI_DB_PASS]")
	RootCmd.PersistentFlags().StringVar(&flags.DatabaseName, "db-name", dbNameEnv, "Database name (for postgres) [env: KOMARI_DB_NAME]")
}
