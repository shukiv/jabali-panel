<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::table('users', function (Blueprint $table) {
            $table->unsignedInteger('cpu_quota')->nullable()->after('disk_quota_mb')
                ->comment('Per-user CPU override, null = inherit from package');
            $table->unsignedInteger('memory_limit_mb')->nullable()->after('cpu_quota')
                ->comment('Per-user memory override in MB, null = inherit from package');
            $table->unsignedInteger('io_read_mbps')->nullable()->after('memory_limit_mb')
                ->comment('Per-user I/O read override in MB/s, null = inherit from package');
            $table->unsignedInteger('io_write_mbps')->nullable()->after('io_read_mbps')
                ->comment('Per-user I/O write override in MB/s, null = inherit from package');
            $table->unsignedInteger('max_processes')->nullable()->after('io_write_mbps')
                ->comment('Per-user process limit override, null = inherit from package');
        });
    }

    public function down(): void
    {
        Schema::table('users', function (Blueprint $table) {
            $table->dropColumn([
                'cpu_quota',
                'memory_limit_mb',
                'io_read_mbps',
                'io_write_mbps',
                'max_processes',
            ]);
        });
    }
};
