<?php

declare(strict_types=1);

namespace App\Services\System;

use App\Models\EmailDomain;
use App\Models\EmailForwarder;
use App\Models\Mailbox;
use App\Services\Agent\AgentClient;

class MailRoutingSyncService
{
    public function sync(): void
    {
        $domains = EmailDomain::query()
            ->where('is_active', true)
            ->with('domain')
            ->get()
            ->map(fn ($domain) => $domain->domain?->domain)
            ->filter()
            ->unique()
            ->values()
            ->toArray();

        $mailboxes = Mailbox::query()
            ->where('is_active', true)
            ->with('emailDomain.domain')
            ->get()
            ->map(function (Mailbox $mailbox): array {
                return [
                    'email' => $mailbox->email,
                    'path' => $mailbox->maildir_path,
                ];
            })
            ->toArray();

        $aliases = EmailForwarder::query()
            ->where('is_active', true)
            ->with('emailDomain.domain')
            ->get()
            ->map(function (EmailForwarder $forwarder): array {
                return [
                    'source' => $forwarder->email,
                    'destinations' => $forwarder->destinations ?? [],
                ];
            })
            ->toArray();

        $catchAll = EmailDomain::query()
            ->where('catch_all_enabled', true)
            ->with('domain')
            ->get()
            ->map(function (EmailDomain $domain): array {
                return [
                    'source' => '@'.$domain->domain->domain,
                    'destinations' => $domain->catch_all_address ? [$domain->catch_all_address] : [],
                ];
            })
            ->filter(fn (array $entry) => ! empty($entry['destinations']))
            ->toArray();

        $aliases = array_merge($aliases, $catchAll);

        // Both legacy (Postfix hash files) and stalwart (JMAP provisioning) backends
        // are handled at the agent dispatch level — the agent routes to the correct handler.
        $agent = app(AgentClient::class);
        $agent->emailSyncMaps($domains, $mailboxes, $aliases);
    }
}
