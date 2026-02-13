<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('mysql_credentials', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->constrained()->onDelete('cascade');
            $table->string('mysql_username');
            $table->text('mysql_password_encrypted');
            $table->timestamps();
            
            $table->unique(['user_id', 'mysql_username']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('mysql_credentials');
    }
};
