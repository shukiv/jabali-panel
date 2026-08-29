-- GH #878: per-app media mapping for Python apps (Django MEDIA_URL/MEDIA_ROOT).
-- nginx serves <base_uri><media_url> from <app_root>/<media_root> directly,
-- alongside the existing static split. NOT NULL DEFAULT '' matches static_*.
ALTER TABLE python_apps
  ADD COLUMN media_url VARCHAR(255) NOT NULL DEFAULT '',
  ADD COLUMN media_root VARCHAR(512) NOT NULL DEFAULT '';
