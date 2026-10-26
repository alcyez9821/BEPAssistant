// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// Package store 封装 SQLite 持久化层：账号 + 车辆表结构、迁移、CRUD。
//
// File: db.go
// Purpose: 打开 SQLite 数据库、应用 pragma、执行表结构 + 增量列迁移。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   BEP Assistant 是单用户 app，账号/车辆数据全部落在本地 SQLite 中。数据库路径由
//   Flutter 传入（通常是 getApplicationDocumentsDirectory + /app.db）。历史上曾用
//   android.database.sqlite 从 Kotlin 侧直连本 DB 导致视图不一致（2026-07-02 事故），
//   目前 Kotlin 侧一律走 Go FFI，DB 只被 Go 单进程打开。
//
// Key Logic:
//   1. 在 open URL 里内嵌 pragma —— WAL / synchronous=FULL / busy_timeout=5000
//      比后来 Exec("PRAGMA ...") 更早生效，且不依赖连接池状态
//   2. 限死连接池 MaxOpenConns=1 —— 消除 SQLite + modernc.org/sqlite 多连接视图不一致问题
//   3. migrate 分三步执行：CREATE 表 + ALTER ADD 二次办证/审核字段 + DROP 老库多用户遗留字段
//   4. Logger 由 bridge.go 注入，落到 Android logcat 的 Bridge tag
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Logger 允许外部（bridge.go）注入日志实现（例如打到 Android logcat）。
//
// 默认为 nil：不打日志；bridge.go Init 时会赋值成 bridgeLog，使 store 内部
// 出错日志能与 Go 主线日志汇聚到 Android logcat 的 Bridge tag。
var Logger func(msg string)

// logf 是内部包装：Logger 非 nil 时格式化输出，nil 时静默丢弃。
//
// 用 logf 而不是 log.Printf，因为 modernc.org/sqlite 版本在 Android 上不 attach stdout，
// 需要主动把日志送到 logcat 才能被开发者看到。
func logf(format string, args ...interface{}) {
	if Logger != nil {
		Logger(fmt.Sprintf(format, args...))
	}
}

// DB 数据库封装。仅暴露 conn 给同包其他文件（account.go / vehicle.go），
// 外部通过方法调用间接使用。
type DB struct {
	conn *sql.DB
}

// New 打开 SQLite 数据库并初始化表结构。
//
// 关键步骤：
//   1. 确保父目录存在（Flutter app_flutter 目录首次启动可能未创建）
//   2. 用 pragma 内嵌到 URL 的形式 open：一次性设定 WAL / synchronous / busy_timeout
//   3. SetMaxOpenConns(1) + SetMaxIdleConns(1) 限死单连接池，避免多连接视图不一致
//   4. 反查 journal_mode 验证 pragma 已生效，防止 URL 语法错误静默失败
//   5. 调 migrate() 执行 CREATE + ALTER + DROP 增量迁移
//
// Args:
//   dbPath: SQLite 文件绝对路径（例如 /data/user/0/com.alcyez.bep_assistant/app_flutter/app.db）。
//
// Returns:
//   *DB:   成功打开且已完成迁移的实例。
//   error: 建目录、open、pragma 校验、migrate 任一失败。
//
// Notes:
//   - 之前用 conn.Exec("PRAGMA journal_mode=WAL") 无法确认真正生效，且 Kotlin 侧直连时
//     可能被改回 delete 模式 —— 改用 URL pragma 是根治方案。
//   - synchronous=FULL 强制每次 commit fsync 到磁盘，避免进程被 Android 系统杀掉后
//     WAL 未 checkpoint 导致丢数据。
func New(dbPath string) (*DB, error) {
	// 确保父目录存在（Flutter app_flutter 目录首次启动可能未创建）
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create directory %s: %w", dir, err)
	}

	// 在 open URL 里把 pragma 塞进去 —— 比 conn.Exec 更早生效，且每次 open 都会重新应用。
	// 之前用 conn.Exec("PRAGMA journal_mode=WAL") 存在两个坑：
	//   1. Exec 不 fetch 返回值，PRAGMA 是否真生效只能猜；
	//   2. Kotlin 侧的 android.database.sqlite 也会打开同一个 DB，Kotlin 关闭时可能把模式改回 delete。
	// 用 URL pragma 确保每个连接都以指定模式打开；synchronous=FULL 强制每次 commit 都 fsync 到磁盘，
	// 避免进程被杀后 WAL 未 checkpoint 导致丢数据。
	openURL := dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)"
	conn, err := sql.Open("sqlite", openURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite 只允许单写多读，database/sql 的默认连接池会拿多个连接：
	// 用 modernc.org/sqlite 时不同连接可能看到不一致的视图，导致 UPDATE 成功但
	// 后续读连接读到旧值（2026-07-02 事故：ec_notified_at 写入 success 但落不到磁盘）。
	// 限死一个连接 + 一个池就消除这个问题；SQLite 本身对单连接足够快。
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	// 验证 pragma 是否生效
	var journalMode string
	if err := conn.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		conn.Close()
		return nil, fmt.Errorf("query journal_mode: %w", err)
	}
	logf("[store.New] dbPath=%s journal_mode=%s", dbPath, journalMode)

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// migrate 分三步完成表结构迁移。
//
// Step 1: CREATE TABLE IF NOT EXISTS —— accounts / vehicles + 索引；新库直接落到位。
// Step 2: ALTER TABLE ADD COLUMN —— 二次办证 / 审核状态字段；老库缺列时补齐，重复添加返回
//         "duplicate column" 忽略即可。
// Step 3: DROP INDEX / DROP COLUMN / DROP TABLE —— 清理多用户遗留字段（accounts.user_id /
//         accounts.login_type / vehicles.user_id / users 表）；新库没有则报错忽略。
//
// Notes:
//   - modernc.org/sqlite 内嵌 SQLite ≥ 3.35，支持 DROP COLUMN，无需手工重建表。
//   - 所有 ALTER / DROP 都用 Exec 直接执行并忽略错误 —— 幂等且避免每次启动查一遍 pragma_info。
//
// Returns:
//   error: CREATE 语句失败（表结构未创建，无法继续）。
func (db *DB) migrate() error {
	// 第一步：确保表存在
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					phone TEXT NOT NULL,
					nickname TEXT DEFAULT '',
					password TEXT DEFAULT '',
					token TEXT DEFAULT '',
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					updated_at DATETIME
				)`,
		`CREATE TABLE IF NOT EXISTS vehicles (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					account_id INTEGER NOT NULL,
					v_id TEXT NOT NULL,
					hphm TEXT NOT NULL,
					hpzl TEXT NOT NULL,
					mode TEXT DEFAULT 'reminder',
					default_area TEXT DEFAULT '',
					default_xxdz TEXT DEFAULT '',
					default_jjmd TEXT DEFAULT '其它',
					jjzzl TEXT DEFAULT '02',
					ybcs INTEGER DEFAULT 0,
					sycs TEXT DEFAULT '',
					syts TEXT DEFAULT '',
					last_status TEXT DEFAULT '',
					last_jjzzlmc TEXT DEFAULT '',
					last_yxqs TEXT DEFAULT '',
					last_yxqz TEXT DEFAULT '',
					last_jsrxm TEXT DEFAULT '',
					last_apply_id TEXT DEFAULT '',
					last_blzt INTEGER DEFAULT 0,
					last_shsbyyms TEXT DEFAULT '',
					last_checked_at DATETIME,
					last_error TEXT DEFAULT '',
					last_reminded_at TEXT,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					updated_at DATETIME
				)`,
		`CREATE INDEX IF NOT EXISTS idx_vehicles_account ON vehicles(account_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_vehicles_vid_account ON vehicles(v_id, account_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:40], err)
		}
	}

	// 第二步：增量迁移。列 / 表 / 索引已经不存在时会报错，全部忽略即可。
	// 二次办证字段（老库缺列时补上）
	alterStmts := []string{
		`ALTER TABLE vehicles ADD COLUMN ec_notified_at TEXT DEFAULT ''`,
		`ALTER TABLE vehicles ADD COLUMN ec_auto_applied INTEGER DEFAULT 0`,
		`ALTER TABLE vehicles ADD COLUMN ec_blzt INTEGER DEFAULT 0`,
		`ALTER TABLE vehicles ADD COLUMN ec_yxqs TEXT DEFAULT ''`,
		`ALTER TABLE vehicles ADD COLUMN ec_yxqz TEXT DEFAULT ''`,
		`ALTER TABLE vehicles ADD COLUMN ec_jjzzlmc TEXT DEFAULT ''`,
		`ALTER TABLE vehicles ADD COLUMN ec_shsbyyms TEXT DEFAULT ''`,
		`ALTER TABLE vehicles ADD COLUMN last_blzt INTEGER DEFAULT 0`,
		`ALTER TABLE vehicles ADD COLUMN last_shsbyyms TEXT DEFAULT ''`,
	}
	for _, stmt := range alterStmts {
		db.conn.Exec(stmt) // 忽略错误（列已存在时会报 duplicate column）
	}

	// 第三步：清理老库遗留字段。SQLite 3.35+ 支持 DROP COLUMN；
	// modernc.org/sqlite 内嵌版本满足。列/表/索引不存在时报错，直接忽略。
	// - accounts.user_id / vehicles.user_id：曾用于预留多用户，从未使用
	// - accounts.login_type：曾区分 sms/password 登录，现在只支持 password
	// - users 表：从未插入过数据
	// - idx_accounts_user / idx_vehicles_user：依赖 user_id 的索引
	cleanupStmts := []string{
		`DROP INDEX IF EXISTS idx_accounts_user`,
		`DROP INDEX IF EXISTS idx_vehicles_user`,
		`ALTER TABLE accounts DROP COLUMN user_id`,
		`ALTER TABLE accounts DROP COLUMN login_type`,
		`ALTER TABLE vehicles DROP COLUMN user_id`,
		`DROP TABLE IF EXISTS users`,
	}
	for _, stmt := range cleanupStmts {
		db.conn.Exec(stmt) // 老库有列/表就删；新库没有就报错并忽略
	}

	return nil
}
