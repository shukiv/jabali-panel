<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('backup_restores', function (Blueprint $table) {
            $table->id();
            $table->foreignId('backup_id')->constrained()->cascadeOnDelete();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            $table->boolean('restore_files')->default(true);
            $table->boolean('restore_databases')->default(true);
            $table->boolean('restore_mailboxes')->default(true);
            $table->boolean('restore_dns')->default(true);
            $table->json('selected_domains')->nullable(); // Specific domains to restore
            $table->json('selected_databases')->nullable(); // Specific databases to restore
            $table->json('selected_mailboxes')->nullable(); // Specific mailboxes to restore
            $table->enum('status', ['pending', 'downloading', 'running', 'completed', 'failed'])->default('pending');
            $table->unsignedTinyInteger('progress')->default(0); // 0-100
            $table->text('log')->nullable();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('completed_at')->nullable();
            $table->text('error_message')->nullable();
            $table->json('result')->nullable(); // Summary of what was restored
            $table->timestamps();

            $table->index(['backup_id', 'status']);
            $table->index(['user_id', 'status']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('backup_restores');
    }
};
