-- GH #878: per-app static-asset split for Python apps. nginx serves
-- <base_uri><static_url> directly from <app_root>/<static_root> instead of
-- proxying to gunicorn/uvicorn — the Passenger public/ equivalent for
-- hand-configured apps. Framework installs seed these from the catalog;
-- empty means everything proxies to the app (previous behavior).
ALTER TABLE python_apps
  ADD COLUMN static_url  varchar(255) NOT NULL DEFAULT '',
  ADD COLUMN static_root varchar(512) NOT NULL DEFAULT '';
