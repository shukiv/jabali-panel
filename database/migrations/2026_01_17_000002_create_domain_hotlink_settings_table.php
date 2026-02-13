<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('domain_hotlink_settings', function (Blueprint $table) {
            $table->id();
            $table->foreignId('domain_id')->unique()->constrained()->cascadeOnDelete();
            $table->boolean('is_enabled')->default(false);
            $table->text('allowed_domains')->nullable();
            $table->boolean('block_blank_referrer')->default(true);
            $table->string('protected_extensions')->default('jpg,jpeg,png,gif,webp,svg,mp4,mp3,pdf');
            $table->string('redirect_url')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('domain_hotlink_settings');
    }
};
