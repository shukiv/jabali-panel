<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Run the migrations.
     */
    public function up(): void
    {
        Schema::create('user_remote_backups', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->constrained()->onDelete('cascade');
            $table->foreignId('destination_id')->constrained('backup_destinations')->onDelete('cascade');
            $table->string('backup_name'); // Timestamp directory name: 2026-01-19_230002
            $table->string('backup_path'); // User's path: 2026-01-19_230002/user
            $table->string('backup_type')->default('incremental'); // incremental or full
            $table->timestamp('backup_date')->nullable(); // Parsed from backup_name
            $table->timestamp('indexed_at')->nullable();
            $table->timestamps();

            // Unique constraint: one entry per user per backup per destination
            $table->unique(['user_id', 'destination_id', 'backup_name'], 'user_dest_backup_unique');
            $table->index(['user_id', 'destination_id']);
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::dropIfExists('user_remote_backups');
    }
};
