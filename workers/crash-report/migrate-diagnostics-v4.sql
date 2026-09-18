-- Additive diagnostics v4 migration. Apply before the matching Worker.
ALTER TABLE groups ADD COLUMN regression_review TEXT NOT NULL DEFAULT '';
ALTER TABLE groups ADD COLUMN resolution_platform TEXT NOT NULL DEFAULT '';
ALTER TABLE groups ADD COLUMN resolution_runtime TEXT NOT NULL DEFAULT '';
ALTER TABLE groups ADD COLUMN resolution_basis TEXT NOT NULL DEFAULT '';
ALTER TABLE groups ADD COLUMN last_category TEXT NOT NULL DEFAULT '';

ALTER TABLE reports ADD COLUMN event_id TEXT NOT NULL DEFAULT '';
ALTER TABLE reports ADD COLUMN incident_id TEXT NOT NULL DEFAULT '';
ALTER TABLE reports ADD COLUMN diagnostics TEXT NOT NULL DEFAULT '';
ALTER TABLE reports ADD COLUMN error_family TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS reports_event_id ON reports (event_id);

CREATE TABLE IF NOT EXISTS report_events (
  event_id TEXT PRIMARY KEY,
  incident_id TEXT NOT NULL DEFAULT '',
  fingerprint TEXT NOT NULL,
  received_at TEXT NOT NULL,
  projected_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS report_events_fingerprint ON report_events (fingerprint, received_at);
CREATE INDEX IF NOT EXISTS report_events_incident ON report_events (incident_id, received_at);

CREATE TABLE IF NOT EXISTS report_attribution_daily (
  date TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  subject_version TEXT NOT NULL DEFAULT '',
  observer_version TEXT NOT NULL DEFAULT '',
  subject_channel TEXT NOT NULL DEFAULT '',
  observer_channel TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT '',
  evidence TEXT NOT NULL DEFAULT '',
  events INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (date, fingerprint, subject_version, observer_version, subject_channel, observer_channel, category, evidence)
);
CREATE INDEX IF NOT EXISTS report_attribution_fingerprint_date ON report_attribution_daily (fingerprint, date);

CREATE TABLE IF NOT EXISTS report_incidents (
  date TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  incident_id TEXT NOT NULL,
  subject_version TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (date, fingerprint, incident_id)
);
CREATE INDEX IF NOT EXISTS report_incidents_fingerprint_date ON report_incidents (fingerprint, date);

INSERT OR IGNORE INTO diagnostics_meta (key, value)
VALUES ('structured_attribution_since', date('now'));
