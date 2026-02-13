<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('backups', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            $table->foreignId('destination_id')->nullable()->constrained('backup_destinations')->nullOnDelete();
            $table->string('name');
            $table->string('filename');
            $table->enum('type', ['full', 'partial', 'server']); // server = full server backup by admin
            $table->boolean('include_files')->default(true);
            $table->boolean('include_databases')->default(true);
            $table->boolean('include_mailboxes')->default(true);
            $table->boolean('include_dns')->default(true);
            $table->json('domains')->nullable(); // Which domains included
            $table->json('databases')->nullable(); // Which databases included
            $table->json('mailboxes')->nullable(); // Which mailboxes included
            $table->json('users')->nullable(); // For server backups: which users included
            $table->bigInteger('size_bytes')->default(0);
            $table->integer('file_count')->default(0);
            $table->enum('status', ['pending', 'running', 'uploading', 'completed', 'failed'])->default('pending');
            $table->string('local_path')->nullable();
            $table->string('remote_path')->nullable();
            $table->string('checksum')->nullable();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('completed_at')->nullable();
            $table->text('error_message')->nullable();
            $table->json('metadata')->nullable(); // Version, compression, etc.
            $table->timestamp('expires_at')->nullable(); // For retention
            $table->timestamps();

            $table->index(['user_id', 'status']);
            $table->index(['status', 'created_at']);
            $table->index(['type', 'status']);
            $table->index('expires_at');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('backups');
    }
};
