-- RemoteAssist 协助者端本地数据库结构（schema_version = 1）
-- 由 store.Open 通过 go:embed 内嵌执行；所有语句均为幂等（IF NOT EXISTS），
-- 因此重复启动不会破坏已有数据。

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- 控制台账号。
-- role：1 观察者（只读）/ 2 普通用户（可操作）/ 3 管理员（上帝权限）
-- password_hash 为 PBKDF2-HMAC-SHA256 派生结果，salt 每账号独立随机，
-- iterations 单独存列，便于日后提高强度时老账号仍可校验。
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    display_name  TEXT    NOT NULL DEFAULT '',
    password_hash TEXT    NOT NULL,
    salt          TEXT    NOT NULL,
    iterations    INTEGER NOT NULL,
    role          INTEGER NOT NULL DEFAULT 1,
    disabled      INTEGER NOT NULL DEFAULT 0,
    token_version INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    last_login_at INTEGER NOT NULL DEFAULT 0
);

-- 连接过的被控机。
-- min_role：能看到并操作该机器所需的最低角色（默认 2=普通用户，
-- 即观察者默认看不到任何机器，需要管理员按需下调到 1）。
CREATE TABLE IF NOT EXISTS bots (
    id         TEXT PRIMARY KEY,
    name       TEXT    NOT NULL DEFAULT '',
    os         TEXT    NOT NULL DEFAULT '',
    min_role   INTEGER NOT NULL DEFAULT 2,
    note       TEXT    NOT NULL DEFAULT '',
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL
);

-- 登录会话。JWT 只携带 sid，真正的有效期与撤销状态以本表为准：
-- 登出写 revoked_at，改密码/改权限/停用账号则 users.token_version + 1。
CREATE TABLE IF NOT EXISTS sessions (
    id            TEXT PRIMARY KEY,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_version INTEGER NOT NULL,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    revoked_at    INTEGER NOT NULL DEFAULT 0,
    ip            TEXT    NOT NULL DEFAULT '',
    user_agent    TEXT    NOT NULL DEFAULT ''
);

-- 审计日志：登录/登出，以及用户与机器等级的每一次变更。
-- 不设外键，账号删除后历史记录仍然保留。
CREATE TABLE IF NOT EXISTS audit_log (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    at       INTEGER NOT NULL,
    user_id  INTEGER NOT NULL DEFAULT 0,
    username TEXT    NOT NULL DEFAULT '',
    action   TEXT    NOT NULL,
    target   TEXT    NOT NULL DEFAULT '',
    detail   TEXT    NOT NULL DEFAULT '',
    ip       TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_user    ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_audit_at         ON audit_log(at);
CREATE INDEX IF NOT EXISTS idx_users_role       ON users(role);
