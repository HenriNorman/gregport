DROP TABLE IF EXISTS export_jobs;

DROP INDEX IF EXISTS idx_attachments_org_status_expires;
DROP INDEX IF EXISTS idx_attachments_storage_key;

ALTER TABLE attachments
  DROP CONSTRAINT IF EXISTS attachments_upload_status_check,
  DROP COLUMN IF EXISTS upload_status,
  DROP COLUMN IF EXISTS confirmed_at,
  DROP COLUMN IF EXISTS expires_at,
  DROP COLUMN IF EXISTS thumbnail_key,
  DROP COLUMN IF EXISTS checksum,
  DROP COLUMN IF EXISTS quarantine_reason,
  DROP COLUMN IF EXISTS updated_at;
