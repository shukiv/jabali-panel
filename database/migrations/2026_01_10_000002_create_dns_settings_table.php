<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('dns_settings', function (Blueprint $table) {
            $table->id();
            $table->string('key')->unique();
            $table->text('value')->nullable();
            $table->timestamps();
        });

        DB::table('dns_settings')->insert([
            ['key' => 'ns1', 'value' => 'ns1.example.com', 'created_at' => now(), 'updated_at' => now()],
            ['key' => 'ns2', 'value' => 'ns2.example.com', 'created_at' => now(), 'updated_at' => now()],
            ['key' => 'default_ip', 'value' => '127.0.0.1', 'created_at' => now(), 'updated_at' => now()],
            ['key' => 'default_ttl', 'value' => '3600', 'created_at' => now(), 'updated_at' => now()],
            ['key' => 'admin_email', 'value' => 'admin.example.com', 'created_at' => now(), 'updated_at' => now()],
        ]);
    }

    public function down(): void
    {
        Schema::dropIfExists('dns_settings');
    }
};
