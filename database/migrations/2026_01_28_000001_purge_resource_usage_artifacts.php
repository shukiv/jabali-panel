<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        if (Schema::hasTable('user_resource_usages')) {
            Schema::drop('user_resource_usages');
        }

        if (Schema::hasTable('user_resource_limits')) {
            Schema::drop('user_resource_limits');
        }

        if (Schema::hasTable('hosting_packages')) {
            $columns = [];
            foreach (['cpu_limit_percent', 'memory_limit_mb', 'io_limit_mb'] as $column) {
                if (Schema::hasColumn('hosting_packages', $column)) {
                    $columns[] = $column;
                }
            }

            if (! empty($columns)) {
                Schema::table('hosting_packages', function (Blueprint $table) use ($columns) {
                    $table->dropColumn($columns);
                });
            }
        }

        if (Schema::hasTable('dns_settings')) {
            DB::table('dns_settings')->where('key', 'resource_limits_enabled')->delete();
        }

        if (Schema::hasTable('user_settings')) {
            DB::table('user_settings')
                ->whereIn('key', [
                    'bandwidth_total_bytes',
                    'disk_io_total_bytes',
                    'cpu_usage_usec_total',
                    'cpu_usage_captured_at',
                ])
                ->delete();
        }
    }

    public function down(): void
    {
        // Irreversible purge of legacy resource usage artifacts.
    }
};
