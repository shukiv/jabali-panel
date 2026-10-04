-- Reverse GH #1701 per-package disabled PHP functions.
ALTER TABLE hosting_packages
  DROP COLUMN php_disabled_functions;
