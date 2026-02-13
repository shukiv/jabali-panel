<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('backup_destinations', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            $table->string('name');
            $table->enum('type', ['local', 'sftp', 'nfs', 's3']);
            $table->text('config')->nullable(); // Encrypted JSON: host, port, path, credentials
            $table->boolean('is_default')->default(false);
            $table->boolean('is_active')->default(true);
            $table->boolean('is_server_backup')->default(false); // For admin server-wide backups
            $table->timestamp('last_tested_at')->nullable();
            $table->enum('test_status', ['pending', 'success', 'failed'])->nullable();
            $table->text('test_message')->nullable();
            $table->timestamps();

            $table->index(['user_id', 'is_active']);
            $table->index(['is_server_backup', 'is_active']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('backup_destinations');
    }
};
