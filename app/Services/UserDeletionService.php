<?php

declare(strict_types=1);

namespace App\Services;

use App\Models\MysqlCredential;
use App\Models\User;
use App\Services\Agent\AgentClient;
use Illuminate\Support\Facades\Log;

class UserDeletionService
{
    public function __construct(private AgentClient $agent) {}

    public function delete(User $user): void
    {
        $this->beforeDelete($user);
        $user->delete();
    }

    public function beforeDelete(User $user): void
    {
        if ($user->is_admin && (int) $user->getKey() === 1) {
            throw new \RuntimeException(__('Primary admin account cannot be deleted.'));
        }

        $this->cleanupForwarders($user);
        $this->dropMysqlUser($user);
        $this->cleanupCredentials($user);
        $this->removeCgroupSlice($user);
    }

    private function cleanupForwarders(User $user): void
    {
        try {
            $domains = $user->domains()->with('emailDomain.forwarders')->get();

            foreach ($domains as $domain) {
                $forwarders = $domain->emailDomain?->forwarders ?? collect();
                foreach ($forwarders as $forwarder) {
                    $this->agent->send('email.forwarder_delete', [
                        'username' => $user->username,
                        'email' => $forwarder->email,
                    ]);
                }
            }
        } catch (\Throwable $e) {
            Log::warning("Failed to delete email forwarders for user {$user->username}: ".$e->getMessage());
        }
    }

    private function dropMysqlUser(User $user): void
    {
        $masterUser = $user->username.'_admin';

        try {
            if (class_exists(\mysqli::class)) {
                $mysqli = new \mysqli(
                    config('database.connections.mysql.host', 'localhost'),
                    config('database.connections.mysql.username'),
                    config('database.connections.mysql.password')
                );

                if (! $mysqli->connect_error) {
                    if (! preg_match('/^[a-zA-Z0-9_]+$/', $masterUser)) {
                        throw new \Exception('Invalid MySQL username format');
                    }

                    $escapedUser = $mysqli->real_escape_string($masterUser);
                    $mysqli->query("DROP USER IF EXISTS '{$escapedUser}'@'localhost'");
                    $mysqli->close();
                }
            }
        } catch (\Throwable $e) {
            Log::error('Failed to delete master MySQL user: '.$e->getMessage());
        }
    }

    private function cleanupCredentials(User $user): void
    {
        try {
            MysqlCredential::where('user_id', $user->id)->delete();
        } catch (\Throwable $e) {
            Log::error('Failed to delete stored MySQL credentials: '.$e->getMessage());
        }
    }

    private function removeCgroupSlice(User $user): void
    {
        try {
            $this->agent->cgroupRemove($user->username);
        } catch (\Throwable $e) {
            Log::warning("Failed to remove cgroup slice for user {$user->username}: ".$e->getMessage());
        }
    }
}
