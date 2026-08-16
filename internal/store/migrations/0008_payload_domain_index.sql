-- Discovery reads filter on a payload field, which none of the existing indexes
-- help with: idx_le_record_name covers lookup by name, and the query path never
-- reached into the payload at all until now.
--
-- Two indexes, because they answer different shapes of the same question. A
-- participant may declare one domain as a string or several as an array, and
-- the spec allows both — so both forms have to resolve, and a seed that used the
-- other one must not silently become invisible.
CREATE INDEX idx_le_payload_domain ON log_entries ((payload ->> 'domain'))
  WHERE entry_type = 'record';

CREATE INDEX idx_le_payload_gin ON log_entries USING GIN (payload jsonb_path_ops)
  WHERE entry_type = 'record';
