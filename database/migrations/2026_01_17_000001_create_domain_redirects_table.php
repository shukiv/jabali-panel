<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('domain_redirects', function (Blueprint $table) {
            $table->id();
            $table->foreignId('domain_id')->constrained()->cascadeOnDelete();
            $table->string('source_path');
            $table->string('destination_url');
            $table->enum('redirect_type', ['301', '302'])->default('301');
            $table->boolean('is_wildcard')->default(false);
            $table->boolean('is_active')->default(true);
            $table->timestamps();

            $table->index(['domain_id', 'source_path']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('domain_redirects');
    }
};
