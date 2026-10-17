-- GH #2165: how many keys one call of the Redis key browser API may list (its
-- `limit`) or delete becomes a Hosting Package setting, 100 to 5000. The
-- column default is the fixed value every package had before (500), so
-- nothing changes until an admin raises or lowers it. Users with no package
-- keep 500.
ALTER TABLE hosting_packages
  ADD COLUMN redis_keys_per_call INT NOT NULL DEFAULT 500;
