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
        Schema::create('user_shares', function (Blueprint $table) {
            $table->string('from_user', 255);
            $table->string('to_user', 255);
            $table->char('dummy', 1)->default('1');
            $table->primary(['from_user', 'to_user']);
            $table->index('from_user', 'idx_user_shares_from');
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::dropIfExists('user_shares');
    }
};
