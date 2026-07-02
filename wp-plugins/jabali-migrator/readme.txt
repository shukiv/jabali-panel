=== Jabali Migrator ===
Contributors: jabali
Tags: migration, migrate, transfer
Requires at least: 5.6
Requires PHP: 7.4
Stable tag: 0.1.0
License: GPLv2 or later

Migrate this WordPress site into a Jabali panel with no SSH access. Generate a
one-time token; the Jabali panel pulls the database and files over an
authenticated REST API.

== Description ==
FIRST CUT (0.1.0): secure token auth + ping/manifest/db-export/files-manifest/file
endpoints under /wp-json/jabali-migrator/v1/. DB export uses mysqldump with a
temp defaults-file (password never on the command line). Resumable chunked
transport and a pure-PHP export fallback are planned. Pairs with the Jabali
panel's WordPress-plugin migration flow.

== Changelog ==
= 0.1.0 =
* First cut: token auth, ping, manifest, db-export (mysqldump), file endpoints
  with realpath containment.
