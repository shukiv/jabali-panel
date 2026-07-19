-- JAB-164: mark a Python app as provisioned from a framework marketplace entry.
-- framework = catalog slug (empty/NULL for hand-configured apps); scaffolded_at
-- gates the one-shot app.python.scaffold the reconciler runs before first apply.
ALTER TABLE python_apps
    ADD COLUMN framework VARCHAR(32) NULL,
    ADD COLUMN scaffolded_at TIMESTAMP NULL;
