<?php

declare(strict_types=1);

namespace Database\Seeders;

use App\Models\Domain;
use App\Models\HostingPackage;
use App\Models\User;
use Illuminate\Database\Seeder;
use Illuminate\Support\Facades\Hash;

/**
 * Seeds demo data for DEMO_MODE.
 *
 * Run with: php artisan db:seed --class=DemoSeeder
 */
class DemoSeeder extends Seeder
{
    public function run(): void
    {
        // Admin user
        $admin = User::firstOrCreate(
            ['email' => 'admin@demo.jabali-panel.com'],
            [
                'name' => 'Demo Admin',
                'username' => 'admin',
                'password' => Hash::make('demo'),
                'is_admin' => true,
                'is_active' => true,
            ]
        );
        // Force is_admin (not in $fillable)
        $admin->is_admin = true;
        $admin->save();

        // Hosting packages
        $starter = HostingPackage::firstOrCreate(
            ['name' => 'Starter'],
            [
                'description' => 'Basic shared hosting',
                'disk_quota_mb' => 1024,
                'bandwidth_gb' => 50,
                'domains_limit' => 3,
                'databases_limit' => 3,
                'mailboxes_limit' => 10,
                'cpu_quota' => 100,
                'memory_limit_mb' => 512,
                'max_processes' => 50,
                'nginx_req_per_sec' => 50,
                'nginx_connections' => 100,
                'is_active' => true,
            ]
        );

        $pro = HostingPackage::firstOrCreate(
            ['name' => 'Professional'],
            [
                'description' => 'High performance hosting',
                'disk_quota_mb' => 10240,
                'bandwidth_gb' => 500,
                'domains_limit' => 25,
                'databases_limit' => 25,
                'mailboxes_limit' => 100,
                'cpu_quota' => 200,
                'memory_limit_mb' => 2048,
                'max_processes' => 200,
                'nginx_req_per_sec' => 100,
                'nginx_connections' => 200,
                'is_active' => true,
            ]
        );

        // Demo users
        $users = [
            ['name' => 'Sarah Chen', 'username' => 'sarah', 'email' => 'sarah@demo.jabali-panel.com', 'package' => $starter],
            ['name' => 'David Miller', 'username' => 'david', 'email' => 'david@demo.jabali-panel.com', 'package' => $pro],
            ['name' => 'Emma Wilson', 'username' => 'emma', 'email' => 'emma@demo.jabali-panel.com', 'package' => $starter],
        ];

        foreach ($users as $userData) {
            $user = User::firstOrCreate(
                ['email' => $userData['email']],
                [
                    'name' => $userData['name'],
                    'username' => $userData['username'],
                    'password' => Hash::make('demo'),
                    'hosting_package_id' => $userData['package']->id,
                    'disk_quota_mb' => $userData['package']->disk_quota_mb,
                    'is_active' => true,
                ]
            );

            // Add domains for each user
            $domainName = $userData['username'].'-site.com';
            Domain::firstOrCreate(
                ['domain' => $domainName],
                [
                    'user_id' => $user->id,
                    'is_active' => true,
                ]
            );
        }
    }
}
