<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('server_processes', function (Blueprint $table) {
            $table->id();
            $table->string('batch_id', 36)->index();
            $table->integer('rank');
            $table->integer('pid');
            $table->string('user');
            $table->string('command');
            $table->decimal('cpu', 5, 2);
            $table->decimal('memory', 5, 2);
            $table->timestamp('captured_at')->index();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('server_processes');
    }
};
