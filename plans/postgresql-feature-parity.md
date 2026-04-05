# Plan: PostgreSQL Feature Parity

## Objective

Bring PostgreSQL support to parity with MySQL in the panel. Four areas: service management, database CRUD, role/privilege management, backup/restore.

## Current State

- Basic agent functions exist: create/delete databases, create/delete users, change password, grant privileges
- User panel has a PostgreSQL tab on Databases page with create/delete
- **Missing**: privilege UI, revoke, backup/restore, service management, credential storage

## Steps

### Step 1: Add PostgreSQL to Services page
- Add to `$allowedServices` in agent
- Add to admin Services page service list

### Step 2: Agent — privilege and backup functions
- `postgresGetPrivileges(username, database, dbUser)`
- `postgresRevokePrivileges(username, database, dbUser, privileges)`
- `postgresSetPrivileges(username, database, dbUser, privileges)`
- `postgresExportDatabase(username, database, outputPath)`
- `postgresImportDatabase(username, database, inputPath)`
- `postgresGetDatabaseInfo(username, database)` — size, encoding, owner

### Step 3: AgentClient — add PHP wrappers

### Step 4: User panel — privilege management UI
- Expandable per-database privilege grid (like MySQL)
- Grant/revoke per database per user

### Step 5: User panel — backup/restore
- Export database to home dir
- Import from uploaded/home file

### Step 6: Admin panel — PostgreSQL section on Databases resource
