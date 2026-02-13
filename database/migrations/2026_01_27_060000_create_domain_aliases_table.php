<?php

declare(strict_types=1);

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('domain_aliases', function (Blueprint $table) {
            $table->id();
            $table->foreignId('domain_id')->constrained()->cascadeOnDelete();
            $table->string('alias');
            $table->timestamps();

            $table->unique(['domain_id', 'alias']);
            $table->index('alias');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('domain_aliases');
    }
};
