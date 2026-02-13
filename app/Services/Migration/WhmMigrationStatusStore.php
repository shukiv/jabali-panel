<?php

declare(strict_types=1);

namespace App\Services\Migration;

use Illuminate\Support\Facades\Cache;

class WhmMigrationStatusStore
{
    public function __construct(
        public string $cacheKey,
        public int $ttlHours = 2,
    ) {}

    /**
     * @return array<string, mixed>
     */
    public function get(): array
    {
        $state = Cache::get($this->cacheKey);

        return is_array($state) ? $state : [];
    }

    /**
     * @param  array<string, mixed>  $state
     */
    public function put(array $state): void
    {
        Cache::put($this->cacheKey, $state, now()->addHours($this->ttlHours));
    }

    public function clear(): void
    {
        Cache::forget($this->cacheKey);
    }

    /**
     * @param  array<int, string>  $accounts
     * @return array<string, mixed>
     */
    public function initialize(array $accounts): array
    {
        $status = [];

        foreach ($accounts as $user) {
            $status[$user] = [
                'status' => 'pending',
                'log' => [],
                'progress' => 0,
            ];
        }

        $state = [
            'isMigrating' => true,
            'selectedAccounts' => array_values($accounts),
            'migrationStatus' => $status,
            'startedAt' => now()->toDateTimeString(),
            'completedAt' => null,
        ];

        $this->put($state);

        return $state;
    }

    /**
     * @return array<string, mixed>
     */
    public function setMigrating(bool $isMigrating): array
    {
        $state = $this->get();
        $state['isMigrating'] = $isMigrating;

        if (! $isMigrating) {
            $state['completedAt'] = now()->toDateTimeString();
        }

        $this->put($state);

        return $state;
    }

    /**
     * @return array<string, mixed>
     */
    public function updateAccountStatus(string $user, string $status, string $message, string $logStatus = 'info'): array
    {
        $state = $this->get();
        $state = $this->ensureAccount($state, $user);
        $state['migrationStatus'][$user]['status'] = $status;

        $this->appendLog($state, $user, $message, $logStatus);
        $this->put($state);

        return $state;
    }

    /**
     * @return array<string, mixed>
     */
    public function addAccountLog(string $user, string $message, string $status = 'info'): array
    {
        $state = $this->get();
        $state = $this->ensureAccount($state, $user);

        $this->appendLog($state, $user, $message, $status);
        $this->put($state);

        return $state;
    }

    /**
     * @param  array<string, mixed>  $state
     * @return array<string, mixed>
     */
    protected function ensureAccount(array $state, string $user): array
    {
        $state['migrationStatus'] ??= [];
        $state['selectedAccounts'] ??= [];

        if (! isset($state['migrationStatus'][$user])) {
            $state['migrationStatus'][$user] = [
                'status' => 'pending',
                'log' => [],
                'progress' => 0,
            ];
        }

        if (! in_array($user, $state['selectedAccounts'], true)) {
            $state['selectedAccounts'][] = $user;
        }

        return $state;
    }

    /**
     * @param  array<string, mixed>  $state
     */
    protected function appendLog(array &$state, string $user, string $message, string $status): void
    {
        $state['migrationStatus'][$user]['log'][] = [
            'message' => $message,
            'status' => $status,
            'time' => now()->format('H:i:s'),
        ];
    }
}
