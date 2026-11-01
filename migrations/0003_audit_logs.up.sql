CREATE TABLE audit_logs (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  actor_type  text NOT NULL,          
  actor_id    uuid,                   
  action      text NOT NULL,          
  entity_type text NOT NULL,          -- 'invitation' | 'membership' | 'api_key'
  entity_id   uuid,                   -- polymorphic, no FK
  data        jsonb NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_logs_org_created ON audit_logs (org_id, created_at DESC);
CREATE INDEX idx_audit_logs_entity ON audit_logs (entity_type, entity_id);
