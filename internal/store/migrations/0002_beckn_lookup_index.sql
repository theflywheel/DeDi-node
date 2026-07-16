CREATE INDEX idx_le_record_name ON log_entries (record_name, namespace, registry, version_num DESC)
  WHERE entry_type = 'record';
