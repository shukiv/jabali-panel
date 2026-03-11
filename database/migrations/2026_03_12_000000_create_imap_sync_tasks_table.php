<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('imap_sync_tasks', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->constrained()->cascadeOnDelete();
            $table->foreignId('mailbox_id')->constrained()->cascadeOnDelete();
            $table->string('source_host');
            $table->unsignedSmallInteger('source_port')->default(993);
            $table->string('source_encryption')->default('ssl');
            $table->string('source_email');
            $table->text('source_password_encrypted');
            $table->json('folders')->nullable();
            $table->string('batch_id')->nullable()->index();
            $table->string('status')->default('pending');
            $table->unsignedInteger('messages_synced')->default(0);
            $table->unsignedInteger('messages_total')->default(0);
            $table->text('error_message')->nullable();
            $table->string('log_path')->nullable();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('completed_at')->nullable();
            $table->timestamps();

            $table->index(['user_id', 'status']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('imap_sync_tasks');
    }
};
