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
            $table->string('ssh_isolation_mode', 20)->default('disabled')->after('ssh_shell_enabled');
        });

        Schema::table('users', function (Blueprint $table) {
            $table->string('ssh_isolation_mode', 20)->nullable()->after('disk_quota_mb');
        });
    }

    public function down(): void
    {
        Schema::table('hosting_packages', function (Blueprint $table) {
            $table->dropColumn('ssh_isolation_mode');
        });

        Schema::table('users', function (Blueprint $table) {
            $table->dropColumn('ssh_isolation_mode');
        });
    }
};
