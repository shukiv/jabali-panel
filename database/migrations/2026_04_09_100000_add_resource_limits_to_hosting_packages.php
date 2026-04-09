<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::table('hosting_packages', function (Blueprint $table) {
            $table->unsignedInteger('cpu_quota')->nullable()->after('mailboxes_limit')
                ->comment('CPU quota as percentage of one core (100 = 1 core, 200 = 2 cores)');
            $table->unsignedInteger('memory_limit_mb')->nullable()->after('cpu_quota')
                ->comment('Memory hard cap in MB');
            $table->unsignedInteger('io_read_mbps')->nullable()->after('memory_limit_mb')
                ->comment('I/O read limit in MB/s');
            $table->unsignedInteger('io_write_mbps')->nullable()->after('io_read_mbps')
                ->comment('I/O write limit in MB/s');
            $table->unsignedInteger('max_processes')->nullable()->after('io_write_mbps')
                ->comment('Max concurrent processes (pids.max)');
        });
    }

    public function down(): void
    {
        Schema::table('hosting_packages', function (Blueprint $table) {
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
