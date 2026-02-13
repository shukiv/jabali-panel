<?php
namespace App\Console\Commands\Jabali;
use App\Models\User;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\Str;

class UserCommand extends Command {
    protected $signature = 'user {action=list : list|create|show|update|delete|password} {--id= : User ID or email} {--name= : Name} {--email= : Email} {--password= : Password} {--role= : Role} {--force : Skip confirmation}';
    protected $description = 'Manage users: list, create, show, update, delete, password';

    public function handle(): int {
        return match($this->argument('action')) {
            'list' => $this->listUsers(),
            'create' => $this->createUser(),
            'show' => $this->showUser(),
            'update' => $this->updateUser(),
            'delete' => $this->deleteUser(),
            'password' => $this->changePassword(),
            default => $this->error("Unknown action. Use: list, create, show, update, delete, password") ?? 1,
        };
    }

    private function generateSecurePassword(int $length = 16): string {
        $lower = 'abcdefghijklmnopqrstuvwxyz';
        $upper = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ';
        $numbers = '0123456789';
        $special = '!@#$%^&*';
        $password = $lower[random_int(0, 25)] . $upper[random_int(0, 25)] . $numbers[random_int(0, 9)] . $special[random_int(0, 7)];
        $all = $lower . $upper . $numbers . $special;
        for ($i = 4; $i < $length; $i++) $password .= $all[random_int(0, strlen($all) - 1)];
        return str_shuffle($password);
    }

    private function validatePassword(string $password): ?string {
        if (strlen($password) < 8) return 'Password must be at least 8 characters';
        if (!preg_match('/[a-z]/', $password)) return 'Password must contain a lowercase letter';
        if (!preg_match('/[A-Z]/', $password)) return 'Password must contain an uppercase letter';
        if (!preg_match('/[0-9]/', $password)) return 'Password must contain a number';
        return null;
    }

    private function listUsers(): int {
        $users = User::all();
        $this->table(['ID', 'Name', 'Email', 'Role', 'Created'], $users->map(fn($u) => [$u->id, $u->name, $u->email, $u->role ?? 'user', $u->created_at->format('Y-m-d')])->toArray());
        return 0;
    }

    private function createUser(): int {
        $name = $this->option('name') ?? $this->ask('Name');
        $email = $this->option('email') ?? $this->ask('Email');
        $gen = !$this->option('password');
        $password = $this->option('password') ?? $this->generateSecurePassword();
        if ($error = $this->validatePassword($password)) { $this->error($error); return 1; }
        if (User::where('email', $email)->exists()) { $this->error("Email exists!"); return 1; }
        $user = User::create(['name' => $name, 'email' => $email, 'password' => Hash::make($password), 'role' => $this->option('role') ?? 'user', 'email_verified_at' => now()]);
        $this->info("✓ Created user #{$user->id}: {$email}");
        if ($gen) $this->warn("🔑 Password: $password");
        return 0;
    }

    private function showUser(): int {
        $user = $this->findUser();
        if (!$user) return 1;
        $this->table(['Field', 'Value'], [['ID', $user->id], ['Name', $user->name], ['Email', $user->email], ['Role', $user->role ?? 'user'], ['Created', $user->created_at]]);
        return 0;
    }

    private function updateUser(): int {
        $user = $this->findUser();
        if (!$user) return 1;
        $updates = array_filter(['name' => $this->option('name'), 'email' => $this->option('email'), 'role' => $this->option('role')]);
        if (empty($updates)) { $this->warn('Specify --name, --email, or --role'); return 0; }
        $user->update($updates);
        $this->info("✓ Updated user #{$user->id}");
        return 0;
    }

    private function deleteUser(): int {
        $user = $this->findUser();
        if (!$user) return 1;
        if (!$this->option('force') && !$this->confirm("Delete {$user->email}?")) return 0;
        $user->delete();
        $this->info("✓ Deleted user #{$user->id}");
        return 0;
    }

    private function changePassword(): int {
        $user = $this->findUser();
        if (!$user) return 1;
        $gen = !$this->option('password');
        $password = $this->option('password') ?? $this->generateSecurePassword();
        if ($error = $this->validatePassword($password)) { $this->error($error); return 1; }
        $user->update(['password' => Hash::make($password)]);
        $this->info("✓ Password updated for {$user->email}");
        if ($gen) $this->warn("🔑 Password: $password");
        return 0;
    }

    private function findUser(): ?User {
        $id = $this->option('id') ?? $this->ask('User ID or email');
        $user = is_numeric($id) ? User::find($id) : User::where('email', $id)->first();
        if (!$user) $this->error("User not found: $id");
        return $user;
    }
}
