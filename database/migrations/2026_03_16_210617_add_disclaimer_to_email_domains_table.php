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
        Schema::table('email_domains', function (Blueprint $table) {
            $table->boolean('disclaimer_enabled')->default(false);
            $table->text('disclaimer_text')->nullable();
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::table('email_domains', function (Blueprint $table) {
            $table->dropColumn(['disclaimer_enabled', 'disclaimer_text']);
        });
    }
};
