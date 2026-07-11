CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  username TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  must_change_password INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS warp_accounts (
  id INTEGER PRIMARY KEY,
  tag TEXT UNIQUE NOT NULL,
  directory TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  private_key TEXT,
  client_id TEXT,
  access_token TEXT,
  device_id TEXT,
  license_key TEXT,
  peer_public_key TEXT,
  local_address_v4 TEXT,
  local_address_v6 TEXT,
  endpoint_host TEXT,
  endpoint_port INTEGER,
  mtu INTEGER,
  listen_port INTEGER,
  masque_private_key TEXT,
  masque_endpoint_pub_key TEXT,
  masque_endpoint_v4 TEXT,
  masque_endpoint_v6 TEXT,
  last_public_ip TEXT,
  last_colo TEXT,
  last_country TEXT,
  last_latency_ms INTEGER,
  last_speed_bps INTEGER,
  last_packet_loss REAL,
  last_score REAL,
  last_tested_at TEXT,
  traffic_up INTEGER NOT NULL DEFAULT 0,
  traffic_down INTEGER NOT NULL DEFAULT 0,
  is_ip_keeper INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  disabled_reason TEXT
);

CREATE INDEX IF NOT EXISTS idx_warp_accounts_status ON warp_accounts(status);
CREATE INDEX IF NOT EXISTS idx_warp_accounts_public_ip ON warp_accounts(last_public_ip);

CREATE TABLE IF NOT EXISTS proxy_slots (
  id INTEGER PRIMARY KEY,
  username TEXT UNIQUE NOT NULL,
  password TEXT NOT NULL,
  account_id INTEGER REFERENCES warp_accounts(id),
  status TEXT NOT NULL DEFAULT 'active',
  last_error TEXT,
  probe_failures INTEGER NOT NULL DEFAULT 0,
  pinned_public_ip TEXT,
  ip_drift_failures INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_proxy_slots_account ON proxy_slots(account_id);
CREATE INDEX IF NOT EXISTS idx_proxy_slots_status ON proxy_slots(status);

CREATE TABLE IF NOT EXISTS account_tests (
  id INTEGER PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES warp_accounts(id),
  tested_at TEXT NOT NULL,
  public_ip TEXT,
  colo TEXT,
  country TEXT,
  latency_ms INTEGER,
  speed_bps INTEGER,
  packet_loss REAL,
  score REAL,
  error TEXT
);

CREATE INDEX IF NOT EXISTS idx_account_tests_account ON account_tests(account_id);

CREATE TABLE IF NOT EXISTS ip_pool (
  id INTEGER PRIMARY KEY,
  public_ip TEXT UNIQUE NOT NULL,
  keeper_account_id INTEGER REFERENCES warp_accounts(id),
  total_up INTEGER NOT NULL DEFAULT 0,
  total_down INTEGER NOT NULL DEFAULT 0,
  current_up_bps INTEGER NOT NULL DEFAULT 0,
  current_down_bps INTEGER NOT NULL DEFAULT 0,
  last_seen_at TEXT
);

CREATE TABLE IF NOT EXISTS proxy_clients (
  client_ip TEXT PRIMARY KEY,
  username TEXT NOT NULL DEFAULT '',
  account_tag TEXT NOT NULL DEFAULT '',
  total_up INTEGER NOT NULL DEFAULT 0,
  total_down INTEGER NOT NULL DEFAULT 0,
  hit_count INTEGER NOT NULL DEFAULT 0,
  first_seen_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_proxy_clients_last_seen ON proxy_clients(last_seen_at);

CREATE TABLE IF NOT EXISTS traffic_samples (
  id INTEGER PRIMARY KEY,
  sampled_at TEXT NOT NULL,
  up_bps INTEGER NOT NULL DEFAULT 0,
  down_bps INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_traffic_samples_at ON traffic_samples(sampled_at);

CREATE TABLE IF NOT EXISTS schedule_runs (
  id INTEGER PRIMARY KEY,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  detail TEXT,
  accounts_kept INTEGER,
  accounts_disabled INTEGER
);

CREATE INDEX IF NOT EXISTS idx_schedule_runs_started ON schedule_runs(started_at);

CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_nodes (
  id INTEGER PRIMARY KEY,
  node_id TEXT UNIQUE NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  public_ip TEXT NOT NULL DEFAULT '',
  country TEXT NOT NULL DEFAULT '',
  colo TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  last_seen_at TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_nodes_last_seen ON agent_nodes(last_seen_at);
