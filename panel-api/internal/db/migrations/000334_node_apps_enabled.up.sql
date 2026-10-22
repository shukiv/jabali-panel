-- ADR-0175: Node.js apps on the Application Manager (runtime 'node' in
-- python_apps). Opt-in, like Python apps: hidden until an admin enables it in
-- Server Settings -> Apps. Node.js 22 is already on every host, so enabling it
-- installs nothing.
ALTER TABLE server_settings
  ADD COLUMN node_apps_enabled TINYINT(1) NOT NULL DEFAULT 0;
