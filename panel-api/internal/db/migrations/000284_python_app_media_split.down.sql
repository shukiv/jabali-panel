-- Reverse GH #878 python app media split.
ALTER TABLE python_apps
  DROP COLUMN media_url,
  DROP COLUMN media_root;
