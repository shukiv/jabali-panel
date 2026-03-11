<?php

use Illuminate\Database\Migrations\Migration;

return new class extends Migration
{
    public function up(): void
    {
        // SQLite (default) doesn't enforce enum constraints, so source_type = 'hestiacp' works without schema changes.
        // For MySQL: DB::statement("ALTER TABLE server_imports MODIFY source_type ENUM('cpanel','directadmin','hestiacp') NOT NULL");
    }

    public function down(): void {}
};
