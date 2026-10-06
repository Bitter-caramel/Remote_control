-- RemoteAssist 协助者端本地数据库结构（schema_version = 3）
-- 由 store.Open 通过 go:embed 内嵌执行；所有语句均为幂等（IF NOT EXISTS），
-- 因此重复启动不会破坏已有数据，老库升级也无需版本分支。

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

-- ---------------------------------------------------------------------------
-- 操作上下文（Ctx）：一个用户在一台 bot 上的一条独立操作线。
-- 关系是「用户ID → botID → 操作上下文」；被控端为每条上下文起一个独立 shell。
-- ---------------------------------------------------------------------------

-- 上下文本体：1:1 绑定 (bot, owner)。
-- id 由 store.CtxID(botID, ownerID) 确定性生成，与协议 ctxID、内存态三处保持一致。
-- cwd 是该上下文最后一次命令结束时的工作目录，bot 重启后据此重建现场。
CREATE TABLE IF NOT EXISTS contexts (
    id             TEXT PRIMARY KEY,
    bot_id         TEXT    NOT NULL,
    owner_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title          TEXT    NOT NULL DEFAULT '',
    cwd            TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    last_active_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE(bot_id, owner_id)
);

-- 已生效的观看/接续授权：长期有效，直到 owner 主动撤销。
-- mode：watch（只读观看）| operate（可接续输入）
CREATE TABLE IF NOT EXISTS context_grants (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    context_id  TEXT    NOT NULL REFERENCES contexts(id) ON DELETE CASCADE,
    grantee_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mode        TEXT    NOT NULL,
    granted_at  INTEGER NOT NULL,
    UNIQUE(context_id, grantee_id)
);

-- 申请记录：同意后转成 grant，但申请历史保留（state 不可回退）。
CREATE TABLE IF NOT EXISTS context_requests (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    context_id   TEXT    NOT NULL REFERENCES contexts(id) ON DELETE CASCADE,
    requester_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mode         TEXT    NOT NULL,
    state        TEXT    NOT NULL DEFAULT 'pending', -- pending | accepted | rejected
    created_at   INTEGER NOT NULL,
    answered_at  INTEGER NOT NULL DEFAULT 0
);

-- 命令历史回放：按「段（一条命令跑完）」持久化各上下文的原始输出。
-- 重连（helper 重启 / bot 重启）后据此把上次的终端画面重新喂给浏览器，
-- 等价于「关掉终端前的画面还在」。data 为已剥离提示符哨兵的原始字节。
-- 保留量由管理员在「命令历史回放」面板设定（按条数或按天数，二选一），
-- 超出部分立即删除、不可恢复，因此本表不会无限增长。
CREATE TABLE IF NOT EXISTS context_segments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    context_id TEXT    NOT NULL REFERENCES contexts(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    cwd        TEXT    NOT NULL DEFAULT '',
    data       BLOB    NOT NULL,
    at         INTEGER NOT NULL
);

-- 回放保留策略（单行表，id 恒为 1）。
-- mode = count：每条上下文最多保留 max_count 段（默认）；
-- mode = days ：只保留最近 max_days 天内产生的段。
-- 两者语义冲突，故设计为二选一；管理员保存后服务端立即按新策略清理。
CREATE TABLE IF NOT EXISTS replay_settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    mode       TEXT    NOT NULL DEFAULT 'count',
    max_count  INTEGER NOT NULL DEFAULT 200,
    max_days   INTEGER NOT NULL DEFAULT 7,
    updated_at INTEGER NOT NULL DEFAULT 0
);

-- 用户级偏好。单独建表而不是给 users 加列：SQLite 没有
-- ALTER TABLE ... ADD COLUMN IF NOT EXISTS，独立幂等建表最干净。
-- watch_default = 1：该用户所有上下文默认对所有人开放只读。
CREATE TABLE IF NOT EXISTS user_prefs (
    user_id       INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    watch_default INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_contexts_owner   ON contexts(owner_id);
CREATE INDEX IF NOT EXISTS idx_contexts_bot     ON contexts(bot_id);
CREATE INDEX IF NOT EXISTS idx_grants_grantee   ON context_grants(grantee_id);
CREATE INDEX IF NOT EXISTS idx_requests_context ON context_requests(context_id);
CREATE INDEX IF NOT EXISTS idx_ctx_segments_ctx ON context_segments(context_id, seq);
