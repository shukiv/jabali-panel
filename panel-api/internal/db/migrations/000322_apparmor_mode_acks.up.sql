-- GH #2078: an administrator acknowledges an AppArmor profile kept in complain
-- mode on purpose, so the "not enforcing" Server Status alert stops naming it.
-- An acknowledgement holds only while the profile stays in the mode it was
-- given for: the panel deletes it when it changes the profile's mode, and when
-- it reads the profile in another mode. Only complain can be acknowledged; a
-- missing or unconfined profile is a fault, not a choice.
--
-- acknowledged_by is the admin's user id, kept for the record. No foreign key:
-- the acknowledgement outlives a deleted admin. Schema only; no seed rows.
CREATE TABLE apparmor_mode_acks (
  profile         VARCHAR(64) NOT NULL PRIMARY KEY,
  mode            VARCHAR(16) NOT NULL,
  acknowledged_by CHAR(26)    NULL,
  acknowledged_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
