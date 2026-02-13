<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('backup_schedules', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete(); // null = server backup
            $table->foreignId('destination_id')->nullable()->constrained('backup_destinations')->nullOnDelete();
            $table->string('name');
            $table->boolean('is_active')->default(true);
            $table->boolean('is_server_backup')->default(false); // For admin server-wide scheduled backups
            $table->enum('frequency', ['hourly', 'daily', 'weekly', 'monthly']);
            $table->string('time')->default('02:00'); // HH:MM format
            $table->unsignedTinyInteger('day_of_week')->nullable(); // 0-6 for weekly
            $table->unsignedTinyInteger('day_of_month')->nullable(); // 1-31 for monthly
            $table->boolean('include_files')->default(true);
            $table->boolean('include_databases')->default(true);
            $table->boolean('include_mailboxes')->default(true);
            $table->boolean('include_dns')->default(true);
            $table->json('domains')->nullable(); // null = all
            $table->json('databases')->nullable(); // null = all
            $table->json('mailboxes')->nullable(); // null = all
            $table->json('users')->nullable(); // For server backups: null = all users
            $table->unsignedInteger('retention_count')->default(7); // Keep last N backups
            $table->timestamp('last_run_at')->nullable();
            $table->timestamp('next_run_at')->nullable();
            $table->enum('last_status', ['success', 'failed'])->nullable();
            $table->text('last_error')->nullable();
            $table->timestamps();

            $table->index(['user_id', 'is_active']);
            $table->index(['is_server_backup', 'is_active']);
            $table->index(['is_active', 'next_run_at']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('backup_schedules');
    }
};
