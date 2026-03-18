<?php

declare(strict_types=1);

namespace App\Services;

use App\Models\Mailbox;
use App\Models\MailboxShare;
use App\Services\Agent\AgentClient;
use Illuminate\Support\Facades\DB;

/**
 * Agent calls (emailAclShareFolder, emailAclRevokeFolder, emailAclUpdateFolder) are
 * routed at the agent dispatch level to the correct backend (Dovecot ACLs for legacy,
 * Stalwart JMAP sharing for stalwart). No conditional logic needed here.
 */
class MailboxSharingService
{
    protected const ALLOWED_RIGHTS = ['lrs', 'lrswite', 'lrswitekx', 'lrswitekxa'];

    public function __construct(
        protected AgentClient $agent
    ) {}

    public function shareFolder(Mailbox $owner, Mailbox $recipient, string $folder, string $aclRights): MailboxShare
    {
        // Validate same domain
        if ($owner->email_domain_id !== $recipient->email_domain_id) {
            throw new \InvalidArgumentException('Only mailboxes in the same domain can share folders');
        }

        // Validate no self-sharing
        if ($owner->id === $recipient->id) {
            throw new \InvalidArgumentException('Cannot share a folder with itself');
        }

        // Validate ACL rights
        if (! in_array($aclRights, self::ALLOWED_RIGHTS, true)) {
            throw new \InvalidArgumentException('Invalid permission level');
        }

        // Validate folder name
        if ($folder !== 'INBOX' && ! preg_match('/^[A-Za-z0-9._-]+$/', $folder)) {
            throw new \InvalidArgumentException('Invalid folder name');
        }

        return DB::transaction(function () use ($owner, $recipient, $folder, $aclRights) {
            $share = MailboxShare::updateOrCreate(
                [
                    'mailbox_id' => $owner->id,
                    'shared_with_mailbox_id' => $recipient->id,
                    'folder' => $folder,
                ],
                ['acl_rights' => $aclRights]
            );

            // Sync user_shares table for Dovecot dict
            DB::table('user_shares')->updateOrInsert(
                ['from_user' => $owner->email, 'to_user' => $recipient->email],
                ['dummy' => '1']
            );

            // Call agent to write dovecot-acl file
            $username = $owner->emailDomain->domain->user->username;
            $this->agent->emailAclShareFolder(
                $username,
                $owner->email,
                $recipient->email,
                $folder,
                $aclRights
            );

            return $share;
        });
    }

    public function revokeShare(MailboxShare $share): void
    {
        $owner = $share->mailbox;
        $recipient = $share->sharedWith;
        $username = $owner->emailDomain->domain->user->username;

        DB::transaction(function () use ($share, $owner, $recipient, $username) {
            // Call agent to remove from dovecot-acl file
            $this->agent->emailAclRevokeFolder(
                $username,
                $owner->email,
                $recipient->email,
                $share->folder
            );

            // Remove from user_shares if no more shares exist between these users
            $remainingShares = MailboxShare::where('mailbox_id', $owner->id)
                ->where('shared_with_mailbox_id', $recipient->id)
                ->where('id', '!=', $share->id)
                ->exists();

            if (! $remainingShares) {
                DB::table('user_shares')
                    ->where('from_user', $owner->email)
                    ->where('to_user', $recipient->email)
                    ->delete();
            }

            $share->delete();
        });
    }

    public function updatePermissions(MailboxShare $share, string $aclRights): MailboxShare
    {
        // Validate ACL rights
        if (! in_array($aclRights, self::ALLOWED_RIGHTS, true)) {
            throw new \InvalidArgumentException('Invalid permission level');
        }

        $owner = $share->mailbox;
        $recipient = $share->sharedWith;
        $username = $owner->emailDomain->domain->user->username;

        return DB::transaction(function () use ($share, $owner, $recipient, $username, $aclRights) {
            $this->agent->emailAclUpdateFolder(
                $username,
                $owner->email,
                $recipient->email,
                $share->folder,
                $aclRights
            );

            $share->update(['acl_rights' => $aclRights]);

            return $share;
        });
    }
}
