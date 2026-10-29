DROP INDEX sessions_agent_sender_user_session_idx;
CREATE INDEX sessions_agent_sender_user_session_idx
  ON sessions(agent, sender, user, session_id);
