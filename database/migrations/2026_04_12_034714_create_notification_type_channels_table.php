<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('notification_type_channels', function (Blueprint $table) {
            $table->id();
            $table->enum('severity', ['info', 'warning', 'critical']);
            $table->foreignId('channel_id')
                ->constrained('notification_channels')
                ->cascadeOnDelete();
            $table->integer('priority')->default(0);
            $table->timestamps();

            // A given channel may only appear once per severity.
            $table->unique(['severity', 'channel_id'], 'uniq_severity_channel');
            $table->index('severity');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('notification_type_channels');
    }
};
