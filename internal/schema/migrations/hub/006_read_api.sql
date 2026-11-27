CREATE INDEX sessions_api_source_artifact_idx ON sessions(source_id, artifact_id)
  WHERE artifact_role = 'transcript' AND artifact_id <> '';
CREATE INDEX sessions_api_source_group_idx ON sessions
  (source_id, (CASE WHEN agent='claude' THEN coalesce(nullif(native_root_id,''),native_session_id) ELSE native_session_id END))
  WHERE artifact_role = 'transcript' AND artifact_id <> '';
