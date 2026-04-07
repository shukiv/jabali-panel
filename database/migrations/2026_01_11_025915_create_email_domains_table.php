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
        Schema::create('email_domains', function (Blueprint $table) {
            $table->id();
            $table->foreignId('domain_id')->constrained()->cascadeOnDelete();
            $table->boolean('is_active')->default(true);
            $table->string('dkim_selector')->default('default');
            $table->text('dkim_private_key')->nullable();
            $table->text('dkim_public_key')->nullable();
            $table->boolean('catch_all_enabled')->default(false);
            $table->string('catch_all_address')->nullable();
            $table->bigInteger('max_mailboxes')->nullable();
            $table->bigInteger('max_quota_bytes')->default(5368709120); // 5GB default
            $table->timestamps();

            $table->unique('domain_id');
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::dropIfExists('email_domains');
    }
};
