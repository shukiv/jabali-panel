<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('cloudflare_zones', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->constrained()->cascadeOnDelete();
            $table->foreignId('domain_id')->constrained()->cascadeOnDelete();
            $table->string('zone_id');
            $table->string('account_id')->nullable();
            $table->text('api_token');
            $table->timestamps();

            $table->unique(['user_id', 'domain_id']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('cloudflare_zones');
    }
};
