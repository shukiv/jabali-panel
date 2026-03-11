<?php

declare(strict_types=1);

namespace App\Services;

use Illuminate\Support\Facades\Log;
use SQLite3;

class RoundcubeIdentityService
{
    protected ?SQLite3 $db = null;

    protected string $dbPath = '/var/lib/roundcube/roundcube.db';

    /**
     * Add an identity (alias email) to a Roundcube user's account.
     *
     * @param  string  $mailboxEmail  The local mailbox email (Roundcube username)
     * @param  string  $aliasEmail  The forwarder source address to add as identity
     * @param  string  $name  Display name for the identity
     */
    public function addIdentity(string $mailboxEmail, string $aliasEmail, string $name = ''): bool
    {
        $db = $this->getDb();
        if (! $db) {
            return false;
        }

        try {
            // Find the Roundcube user_id for this mailbox
            $userId = $this->getRoundcubeUserId($mailboxEmail);
            if (! $userId) {
                Log::debug("RoundcubeIdentity: No Roundcube user found for {$mailboxEmail}");

                return false;
            }

            // Check if this identity already exists (including soft-deleted)
            $stmt = $db->prepare('SELECT identity_id, del FROM identities WHERE user_id = :user_id AND email = :email');
            $stmt->bindValue(':user_id', $userId, SQLITE3_INTEGER);
            $stmt->bindValue(':email', $aliasEmail, SQLITE3_TEXT);
            $result = $stmt->execute();
            $existing = $result->fetchArray(SQLITE3_ASSOC);

            if ($existing) {
                if ((int) $existing['del'] === 1) {
                    // Re-activate soft-deleted identity
                    $stmt = $db->prepare('UPDATE identities SET del = 0, changed = :changed WHERE identity_id = :id');
                    $stmt->bindValue(':changed', gmdate('Y-m-d H:i:s'), SQLITE3_TEXT);
                    $stmt->bindValue(':id', $existing['identity_id'], SQLITE3_INTEGER);
                    $stmt->execute();
                    Log::info("RoundcubeIdentity: Reactivated identity {$aliasEmail} for {$mailboxEmail}");
                }

                return true;
            }

            // Insert new identity
            $stmt = $db->prepare(
                'INSERT INTO identities (user_id, changed, del, standard, name, email) VALUES (:user_id, :changed, 0, 0, :name, :email)'
            );
            $stmt->bindValue(':user_id', $userId, SQLITE3_INTEGER);
            $stmt->bindValue(':changed', gmdate('Y-m-d H:i:s'), SQLITE3_TEXT);
            $stmt->bindValue(':name', $name, SQLITE3_TEXT);
            $stmt->bindValue(':email', $aliasEmail, SQLITE3_TEXT);
            $stmt->execute();

            Log::info("RoundcubeIdentity: Added identity {$aliasEmail} for {$mailboxEmail}");

            return true;
        } catch (\Exception $e) {
            Log::warning("RoundcubeIdentity: Failed to add identity - {$e->getMessage()}");

            return false;
        }
    }

    /**
     * Remove an identity (alias email) from a Roundcube user's account.
     * Uses soft-delete (del=1) to match Roundcube's convention.
     */
    public function removeIdentity(string $mailboxEmail, string $aliasEmail): bool
    {
        $db = $this->getDb();
        if (! $db) {
            return false;
        }

        try {
            $userId = $this->getRoundcubeUserId($mailboxEmail);
            if (! $userId) {
                return false;
            }

            // Soft-delete the identity (don't remove the default/standard identity)
            $stmt = $db->prepare('UPDATE identities SET del = 1, changed = :changed WHERE user_id = :user_id AND email = :email AND standard = 0');
            $stmt->bindValue(':changed', gmdate('Y-m-d H:i:s'), SQLITE3_TEXT);
            $stmt->bindValue(':user_id', $userId, SQLITE3_INTEGER);
            $stmt->bindValue(':email', $aliasEmail, SQLITE3_TEXT);
            $stmt->execute();

            $affected = $db->changes();
            if ($affected > 0) {
                Log::info("RoundcubeIdentity: Removed identity {$aliasEmail} for {$mailboxEmail}");
            }

            return true;
        } catch (\Exception $e) {
            Log::warning("RoundcubeIdentity: Failed to remove identity - {$e->getMessage()}");

            return false;
        }
    }

    /**
     * Sync identities for a mailbox based on current forwarders.
     * Adds identities for forwarders pointing to this mailbox,
     * removes identities for forwarders that no longer exist.
     *
     * @param  string  $mailboxEmail  The local mailbox email
     * @param  array  $aliasEmails  List of forwarder source emails that forward to this mailbox
     */
    public function syncIdentities(string $mailboxEmail, array $aliasEmails): void
    {
        $db = $this->getDb();
        if (! $db) {
            return;
        }

        $userId = $this->getRoundcubeUserId($mailboxEmail);
        if (! $userId) {
            return;
        }

        // Get current non-default, non-deleted identities
        $stmt = $db->prepare('SELECT email FROM identities WHERE user_id = :user_id AND standard = 0 AND del = 0');
        $stmt->bindValue(':user_id', $userId, SQLITE3_INTEGER);
        $result = $stmt->execute();

        $currentIdentities = [];
        while ($row = $result->fetchArray(SQLITE3_ASSOC)) {
            $currentIdentities[] = strtolower($row['email']);
        }

        $aliasEmails = array_map('strtolower', $aliasEmails);

        // Add missing identities
        foreach ($aliasEmails as $alias) {
            if (! in_array($alias, $currentIdentities, true)) {
                $this->addIdentity($mailboxEmail, $alias);
            }
        }

        // Remove identities that are no longer forwarders
        foreach ($currentIdentities as $identity) {
            if (! in_array($identity, $aliasEmails, true) && $identity !== strtolower($mailboxEmail)) {
                $this->removeIdentity($mailboxEmail, $identity);
            }
        }
    }

    protected function getRoundcubeUserId(string $email): ?int
    {
        $db = $this->getDb();
        if (! $db) {
            return null;
        }

        $stmt = $db->prepare('SELECT user_id FROM users WHERE username = :username');
        $stmt->bindValue(':username', $email, SQLITE3_TEXT);
        $result = $stmt->execute();
        $row = $result->fetchArray(SQLITE3_ASSOC);

        return $row ? (int) $row['user_id'] : null;
    }

    protected function getDb(): ?SQLite3
    {
        if ($this->db !== null) {
            return $this->db;
        }

        if (! file_exists($this->dbPath)) {
            Log::debug('RoundcubeIdentity: Roundcube database not found at '.$this->dbPath);

            return null;
        }

        try {
            $this->db = new SQLite3($this->dbPath);
            $this->db->busyTimeout(5000);

            return $this->db;
        } catch (\Exception $e) {
            Log::warning('RoundcubeIdentity: Could not open Roundcube database - '.$e->getMessage());

            return null;
        }
    }

    public function __destruct()
    {
        if ($this->db !== null) {
            $this->db->close();
        }
    }
}
