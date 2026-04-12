<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('notification_channels', function (Blueprint $table) {
            $table->id();
            $table->string('name', 100)->unique();
            $table->enum('type', ['email', 'telegram', 'ntfy', 'web_push']);
            // Encrypted JSON — encryption is handled at the model layer via the
            // `encrypted:array` cast so the raw column is base64 ciphertext.
            // Keep the column as TEXT because the ciphertext is longer than JSON.
            $table->text('config');
            $table->boolean('enabled')->default(true);
            $table->timestamps();

            $table->index('type');
            $table->index('enabled');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('notification_channels');
    }
};
