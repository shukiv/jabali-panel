<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('email_forwarders', function (Blueprint $table) {
            $table->id();
            $table->foreignId('email_domain_id')->constrained()->cascadeOnDelete();
            $table->foreignId('user_id')->constrained()->cascadeOnDelete();
            $table->string('local_part', 64);
            $table->text('destinations'); // JSON array of destination emails
            $table->boolean('is_active')->default(true);
            $table->timestamps();

            $table->unique(['email_domain_id', 'local_part']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('email_forwarders');
    }
};
