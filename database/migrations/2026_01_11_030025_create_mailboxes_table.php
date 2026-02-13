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
        Schema::create('mailboxes', function (Blueprint $table) {
            $table->id();
            $table->foreignId('email_domain_id')->constrained()->cascadeOnDelete();
            $table->foreignId('user_id')->constrained()->cascadeOnDelete();
            $table->string('local_part'); // Part before @
            $table->string('password_hash');
            $table->string('name')->nullable(); // Display name
            $table->bigInteger('quota_bytes')->default(1073741824); // 1GB default
            $table->bigInteger('quota_used_bytes')->default(0);
            $table->boolean('is_active')->default(true);
            $table->boolean('imap_enabled')->default(true);
            $table->boolean('pop3_enabled')->default(true);
            $table->boolean('smtp_enabled')->default(true);
            $table->timestamp('last_login_at')->nullable();
            $table->timestamps();

            $table->unique(['email_domain_id', 'local_part']);
            $table->index(['local_part', 'email_domain_id']);
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::dropIfExists('mailboxes');
    }
};
