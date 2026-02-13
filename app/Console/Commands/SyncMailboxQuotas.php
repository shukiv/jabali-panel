<?php

declare(strict_types=1);

namespace App\Console\Commands;

use App\Models\Mailbox;
use App\Services\Agent\AgentClient;
use Illuminate\Console\Command;

class SyncMailboxQuotas extends Command
{
    protected $signature = 'jabali:sync-mailbox-quotas';
    protected $description = 'Sync mailbox quota usage from disk to database';

    public function handle(): int
    {
        $mailboxes = Mailbox::with('emailDomain')->get();

        if ($mailboxes->isEmpty()) {
            $this->info('No mailboxes to sync.');
            return 0;
        }

        $this->info("Syncing quota usage for {$mailboxes->count()} mailboxes...");

        $agent = new AgentClient();
        $synced = 0;
        $errors = 0;

        foreach ($mailboxes as $mailbox) {
            try {
                $response = $agent->send('email.mailbox_quota_usage', [
                    'email' => $mailbox->email,
                    'maildir_path' => $mailbox->getRawOriginal('maildir_path'),
                ]);

                $mailbox->quota_used_bytes = $response['quota_used_bytes'] ?? 0;
                $mailbox->save();
                $synced++;
            } catch (\Exception $e) {
                if (str_contains($e->getMessage(), 'not found')) {
                    // Mailbox directory doesn't exist yet (no mail received)
                    $mailbox->quota_used_bytes = 0;
                    $mailbox->save();
                    $synced++;
                } else {
                    $this->error("Failed to sync {$mailbox->email}: {$e->getMessage()}");
                    $errors++;
                }
            }
        }

        $this->info("Synced {$synced} mailboxes" . ($errors ? ", {$errors} errors" : ''));

        return $errors > 0 ? 1 : 0;
    }
}
