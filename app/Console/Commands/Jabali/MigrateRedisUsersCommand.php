<?php

declare(strict_types=1);

namespace App\Console\Commands\Jabali;

use App\Models\User;
use App\Services\Agent\AgentClient;
use Illuminate\Console\Command;

class MigrateRedisUsersCommand extends Command
{
    protected $signature = 'jabali:migrate-redis-users
                            {--dry-run : Show what would be done without making changes}
                            {--force : Recreate credentials even if they already exist}';

    protected $description = 'Create Redis ACL users for existing Jabali users';

    private AgentClient $agent;

    private int $created = 0;

    private int $skipped = 0;

    private int $failed = 0;

    public function __construct()
    {
        parent::__construct();
        $this->agent = app(AgentClient::class);
    }

    public function handle(): int
    {
        $dryRun = $this->option('dry-run');
        $force = $this->option('force');

        $this->info('Migrating existing users to Redis ACL...');

        if ($dryRun) {
            $this->warn('DRY RUN - no changes will be made');
        }

        $this->newLine();

        $users = User::where('role', 'user')->get();

        if ($users->isEmpty()) {
            $this->info('No users found to migrate.');

            return 0;
        }

        $this->info("Found {$users->count()} users to process...");
        $this->newLine();

        foreach ($users as $user) {
            $this->processUser($user, $dryRun, $force);
        }

        $this->newLine();
        $this->info('Migration Complete');
        $this->table(
            ['Metric', 'Count'],
            [
                ['Created', $this->created],
                ['Skipped', $this->skipped],
                ['Failed', $this->failed],
            ]
        );

        return $this->failed > 0 ? 1 : 0;
    }

    private function processUser(User $user, bool $dryRun, bool $force): void
    {
        $homeDir = "/home/{$user->username}";
        $credFile = "{$homeDir}/.redis_credentials";

        // Check if credentials file already exists
        if (file_exists($credFile) && ! $force) {
            $this->line("  [skip] {$user->username} - credentials file already exists");
            $this->skipped++;

            return;
        }

        if ($dryRun) {
            $this->line("  [would create] {$user->username}");
            $this->created++;

            return;
        }

        $this->line("  Processing: {$user->username}...");

        // Generate password before sending to agent so we can save it
        $redisPassword = bin2hex(random_bytes(16)); // 32 char password
        $redisUser = 'jabali_'.$user->username;

        try {
            $result = $this->agent->send('redis.create_user', [
                'username' => $user->username,
                'password' => $redisPassword,
            ]);

            if ($result['success'] ?? false) {
                // Save credentials file
                $credContent = "REDIS_USER={$redisUser}\n".
                               "REDIS_PASS={$redisPassword}\n".
                               "REDIS_PREFIX={$user->username}:\n";

                file_put_contents($credFile, $credContent);
                chmod($credFile, 0600);
                chown($credFile, $user->username);
                chgrp($credFile, $user->username);

                $this->info("    ✓ Created Redis user for {$user->username}");
                $this->created++;
            } else {
                $error = $result['error'] ?? 'Unknown error';
                $this->error("    ✗ Failed for {$user->username}: {$error}");
                $this->failed++;
            }
        } catch (\Exception $e) {
            $this->error("    ✗ Exception for {$user->username}: {$e->getMessage()}");
            $this->failed++;
        }
    }
}
